import math
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

import requests
from locust import LoadTestShape, User, constant_throughput, events, task


@events.init_command_line_parser.add_listener
def _(parser):
    parser.add_argument(
        "--scenario",
        type=str,
        default="normal",
        choices=["normal", "spike", "ramp"],
        help="Pilih skenario traffic: normal | spike | ramp",
    )
    parser.add_argument(
        "--endpoint-path",
        type=str,
        default="/",
        help="Path endpoint yang dipukul (contoh: / atau /api/ping)",
    )


class ScenarioProfile(object):
    name = "base"
    duration_sec = 0

    def target_rps(self, t):
        raise NotImplementedError


class NormalProfile(ScenarioProfile):
    """
    4 menit stabil medium-low di rentang 30-50 RPS.
    Pola dibuat halus via gelombang sinus agar tetap di rentang itu.
    """

    name = "normal"
    duration_sec = 4 * 60

    def target_rps(self, t):
        # 40 +/- 10 => 30..50
        rps = 40 + 10 * math.sin((2 * math.pi / 60.0) * t)
        return max(30, min(50, int(round(rps))))


class SpikeProfile(ScenarioProfile):
    """
    5 menit:
    - menit 0-2: normal 40 RPS
    - menit 2-3: spike 150-200 RPS
    - menit 3-5: kembali normal 40 RPS
    """

    name = "spike"
    duration_sec = 5 * 60

    def target_rps(self, t):
        if t < 120:
            return 40
        if t < 180:
            # 175 +/- 25 => 150..200
            rps = 175 + 25 * math.sin((2 * math.pi / 12.0) * (t - 120))
            return max(150, min(200, int(round(rps))))
        return 40


class RampProfile(ScenarioProfile):
    """
    5 menit:
    - menit 0-3: ramp bertahap 10 -> 180 RPS
    - menit 3-5: tahan di puncak 180 RPS
    """

    name = "ramp"
    duration_sec = 5 * 60

    def target_rps(self, t):
        peak = 180
        if t < 180:
            # Linear ramp 10 -> peak
            rps = 10 + ((peak - 10) * (t / 180.0))
            return max(10, min(peak, int(round(rps))))
        return peak


PROFILES = {
    "normal": NormalProfile(),
    "spike": SpikeProfile(),
    "ramp": RampProfile(),
}


def add_nocache_param(path):
    parts = urlsplit(path)
    query_items = parse_qsl(parts.query, keep_blank_values=True)
    # Tambahkan nonce agar cache perantara tidak menyajikan response lama.
    query_items.append(("nocache", str(int(time.time() * 1000000000))))
    new_query = urlencode(query_items)
    return urlunsplit((parts.scheme, parts.netloc, parts.path, new_query, parts.fragment))


def build_target_url(host, endpoint_path):
    endpoint_path = (endpoint_path or "/").strip()
    if endpoint_path.startswith("http://") or endpoint_path.startswith("https://"):
        return endpoint_path

    if not endpoint_path.startswith("/"):
        endpoint_path = "/" + endpoint_path

    host = (host or "").rstrip("/")
    return host + endpoint_path


class LBUser(User):
    # 1 user ~ 1 request/detik agar total user_count ~= target RPS
    wait_time = constant_throughput(1.0)
    request_timeout_sec = 10

    @task
    def hit_lb(self):
        endpoint = self.environment.parsed_options.endpoint_path
        base_host = self.host or self.environment.host
        if not base_host:
            raise RuntimeError("Host belum di-set. Gunakan --host http://<ip>:<port>")
        url = add_nocache_param(build_target_url(base_host, endpoint))

        headers = {
            "Connection": "close",
            "Cache-Control": "no-cache",
            "Pragma": "no-cache",
        }

        # True cold request:
        # - Buat Session BARU tiap request
        # - Kirim header Connection: close
        # - Tutup response + session di finally
        started = time.perf_counter()
        response = None
        err = None
        response_len = 0
        method = "GET"
        request_name = endpoint if endpoint else "/"
        one_shot_session = requests.Session()

        try:
            response = one_shot_session.get(
                url,
                headers=headers,
                timeout=self.request_timeout_sec,
                allow_redirects=False,
            )
            response_len = len(response.content or b"")
            if response.status_code >= 500:
                err = Exception("HTTP %s" % response.status_code)
        except Exception as exc:
            err = exc
        finally:
            elapsed_ms = (time.perf_counter() - started) * 1000.0
            try:
                if response is not None:
                    response.close()
            finally:
                one_shot_session.close()

            self.environment.events.request.fire(
                request_type=method,
                name=request_name,
                response_time=elapsed_ms,
                response_length=response_len,
                response=response,
                context={},
                exception=err,
            )


class ScenarioShape(LoadTestShape):
    """
    Shape dispatcher untuk menjalankan 3 skenario secara independen
    via argumen --scenario.

    Mekanisme RPS:
    - Setiap user ditargetkan ~1 req/s (constant_throughput=1)
    - Maka user_count diset sama dengan target RPS.
    """

    def _profile(self):
        scenario = self.runner.environment.parsed_options.scenario
        return PROFILES.get(scenario, PROFILES["normal"])

    def _calc_spawn_rate(self, current_users, target_users):
        delta = abs(target_users - current_users)
        # Cukup agresif saat transisi spike, tapi tidak terlalu ekstrem.
        return max(10.0, min(500.0, float(delta if delta > 0 else 10)))

    def _shape_point(self, run_time):
        profile = self._profile()
        if run_time >= profile.duration_sec:
            return None

        target = profile.target_rps(run_time)
        current_users = self.get_current_user_count()
        spawn_rate = self._calc_spawn_rate(current_users, target)
        return (target, spawn_rate)

    def tick(self):
        run_time = self.get_run_time()
        point = self._shape_point(run_time)
        if point is None:
            return None
        return point
