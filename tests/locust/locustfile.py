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
        default="/api/stress-test?ms=50",
        help="Path endpoint yang dipukul (contoh: /api/stress-test?ms=50 atau /api/ping)",
    )


class ScenarioProfile(object):
    name = "base"
    duration_sec = 0

    def target_rps(self, t):
        raise NotImplementedError


class NormalProfile(ScenarioProfile):
    """
    15 menit stabil medium-low di rentang 30-50 RPS.
    Period 140s → dalam 900s ada ~6.4 siklus.
    Train (0-630s): ~4.5 siklus | Test (630-900s): ~2 siklus.
    Keduanya identik secara statistik — mean dan amplitudo sama.
    """

    name = "normal"
    duration_sec = 15 * 60  # 900s

    def target_rps(self, t):
        # period 60s → dalam 900s ada ~15 siklus (asli tidak diubah)
        rps = 40 + 10 * math.sin((2 * math.pi / 60.0) * t)
        return max(30, min(50, int(round(rps))))


class SpikeProfile(ScenarioProfile):
    """
    2 spike identik dalam 15 menit.
    Spike 1 mulai t=120s, Spike 2 mulai t=660s.
    Train (0-630s): baseline + spike pertama + recovery.
    Test (630-900s): baseline + spike kedua + recovery.
    Keduanya punya 1 spike lengkap yang representatif.
    """

    name = "spike"
    duration_sec = 15 * 60  # CHANGED: dari 3 menit → 15 menit penuh

    baseline = 10
    peak = 180
    rise_dur = 30
    hold_dur = 60
    fall_dur = 30
    # total satu spike event = 120s

    def target_rps(self, t):
        def spike_rps(offset):
            """Hitung RPS relatif terhadap awal spike"""
            rise_end = self.rise_dur                    # 30
            hold_end = rise_end + self.hold_dur         # 90
            fall_end = hold_end + self.fall_dur         # 120

            if offset < 0 or offset >= fall_end:
                return None  # di luar window spike
            if offset < rise_end:
                frac = offset / self.rise_dur
                return int(round(self.baseline + frac * (self.peak - self.baseline)))
            if offset < hold_end:
                return self.peak
            frac = (offset - hold_end) / self.fall_dur
            return int(round(self.peak - frac * (self.peak - self.baseline)))

        # CHANGED: 2 spike identik — spike 1 t=120s, spike 2 t=660s
        for spike_start in [120, 660]:
            result = spike_rps(t - spike_start)
            if result is not None:
                return result

        return self.baseline


class RampProfile(ScenarioProfile):
    """
    Sinusoidal siklikal — period 180s (3 menit/siklus) → 5 siklus penuh dalam 900s.
    Train (0-630s): 3.5 siklus | Test (630-900s): 1.5 siklus.
    Range beban identik: 10–75 RPS di kedua zona.
    """

    name = "ramp"
    duration_sec = 15 * 60  # 900s

    def target_rps(self, t):
        low = 10
        peak = 75
        period = 180  # CHANGED: dari 300s → 180s agar test zone dapat siklus lengkap
        mid = (peak + low) / 2        # 42.5
        amplitude = (peak - low) / 2  # 32.5
        # cosinus dimulai dari titik terendah
        rps = mid - amplitude * math.cos((2 * math.pi / period) * t)
        return int(round(max(low, min(peak, rps))))


PROFILES = {
    "normal": NormalProfile(),
    "spike": SpikeProfile(),
    "ramp": RampProfile(),
}


def add_nocache_param(path):
    parts = urlsplit(path)
    query_items = parse_qsl(parts.query, keep_blank_values=True)
    query_items.append(("nocache", str(int(time.time() * 1000000000))))
    new_query = urlencode(query_items)
    return urlunsplit((parts.scheme, parts.netloc, parts.path, new_query, parts.fragment))


def build_target_url(host, endpoint_path):
    endpoint_path = (endpoint_path or "/api/stress-test?ms=50").strip()
    if endpoint_path.startswith("http://") or endpoint_path.startswith("https://"):
        return endpoint_path

    if not endpoint_path.startswith("/"):
        endpoint_path = "/" + endpoint_path

    host = (host or "").rstrip("/")
    return host + endpoint_path


class LBUser(User):
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

        started = time.perf_counter()
        response = None
        err = None
        response_len = 0
        method = "GET"
        request_name = endpoint if endpoint else "/api/stress-test?ms=50"
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
    def _profile(self):
        scenario = self.runner.environment.parsed_options.scenario
        return PROFILES.get(scenario, PROFILES["normal"])

    def _calc_spawn_rate(self, current_users, target_users):
        return 50.0

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