"""
Offline MOPSO + Fuzzy training (aggregated per-second dataset)
Target dataset: 1 row = 1 second sample

Required input columns:
- node1_cpu_normalized_usage
- node1_response_ms
- node1_inflight
- total_requests

Outputs:
- opt_fuzzy_<scenario>.json
- opt_fuzzy_<scenario>.go.txt (Go header snippet)
- optimized_fuzzy_params.json (LB-ready; cocok dengan path default Go)
- optimized_fuzzy_params.go (opsional; Go file siap-compile)
- mopso_report_<scenario>.json
"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Dict, List, Optional, Sequence, Tuple

import numpy as np
import pandas as pd

try:
    import skfuzzy as fuzz
except Exception:
    class _FallbackFuzz(object):
        @staticmethod
        def trimf(x, abc):
            x = np.asarray(x, dtype=np.float64)
            a, b, c = abc
            y = np.zeros_like(x, dtype=np.float64)
            if b > a:
                left = (x > a) & (x < b)
                y[left] = (x[left] - a) / (b - a)
            if c > b:
                right = (x > b) & (x < c)
                y[right] = np.maximum(y[right], (c - x[right]) / (c - b))
            y[x == b] = 1.0
            return np.clip(y, 0.0, 1.0)

    fuzz = _FallbackFuzz()


DIMENSIONS = 27
# Lebih smooth untuk data agregasi per-second (kurangi sensitivitas noise kecil)
NUM_PARTICLES_DEFAULT = 32
ITERATIONS_DEFAULT = 2400
MAX_ARCHIVE = 128
INERTIA_MAX_W = 0.68
INERTIA_MIN_W = 0.52
COGNITIVE_C1 = 0.75
SOCIAL_C2 = 1.15
VELOCITY_CLAMP = 3.5

# Regularisasi ringan agar MF tidak “overfit” fluktuasi kecil pada data agregasi.
PARAM_REG_WEIGHT = 1200.0

WARMUP_ROWS = 5
COOLDOWN_ROWS = 5

COMPILED_RULES: Tuple[Tuple[int, int, int, int], ...] = (
    (0, 0, 0, 2), (0, 0, 1, 2), (0, 0, 2, 1), (0, 1, 0, 2), (0, 1, 1, 1), (0, 1, 2, 1), (0, 2, 0, 1), (0, 2, 1, 1), (0, 2, 2, 0),
    (1, 0, 0, 2), (1, 0, 1, 1), (1, 0, 2, 1), (1, 1, 0, 1), (1, 1, 1, 1), (1, 1, 2, 0), (1, 2, 0, 1), (1, 2, 1, 0), (1, 2, 2, 0),
    (2, 0, 0, 1), (2, 0, 1, 1), (2, 0, 2, 0), (2, 1, 0, 1), (2, 1, 1, 0), (2, 1, 2, 0), (2, 2, 0, 0), (2, 2, 1, 0), (2, 2, 2, 0),
)

OUT_MF = np.array([[0.0, 25.0, 50.0], [25.0, 50.0, 75.0], [50.0, 75.0, 100.0]], dtype=np.float64)

DEFAULT_BASE_PARAMS = np.array(
    [
        0, 40, 75, 60, 80, 95, 85, 95, 100,
        0, 50, 150, 100, 250, 400, 300, 500, 1000,
        0, 150, 300, 200, 500, 800, 600, 850, 1000,
    ],
    dtype=np.float64,
)


@dataclass
class OfflineDataset:
    scenario_name: str
    timestamp: np.ndarray
    node1_cpu_norm: np.ndarray
    node1_response_ms: np.ndarray
    node1_inflight: np.ndarray
    total_requests: np.ndarray

    @property
    def sample_count(self) -> int:
        return int(self.total_requests.shape[0])


@dataclass
class OfflineObjective:
    load_alignment_mse: float
    weighted_sim_rt: float


@dataclass
class OfflineSolution:
    params: List[float]
    objective: OfflineObjective


@dataclass
class OfflineConfig:
    particles: int = NUM_PARTICLES_DEFAULT
    iterations: int = ITERATIONS_DEFAULT
    initial_spread: float = 3.0
    seed: int = 0

    def normalized(self) -> "OfflineConfig":
        particles = self.particles if self.particles > 0 else NUM_PARTICLES_DEFAULT
        iterations = self.iterations if self.iterations > 0 else ITERATIONS_DEFAULT
        spread = self.initial_spread if self.initial_spread > 0 else 3.0
        seed = self.seed if self.seed != 0 else int(time.time_ns())
        return OfflineConfig(particles=particles, iterations=iterations, initial_spread=spread, seed=seed)


def _sanitize_float_array(values: np.ndarray, fallback: float = 0.0) -> np.ndarray:
    arr = np.asarray(values, dtype=np.float64)
    if np.all(np.isfinite(arr)):
        return arr
    finite = arr[np.isfinite(arr)]
    fill = float(np.median(finite)) if finite.size > 0 else float(fallback)
    return np.nan_to_num(arr, nan=fill, posinf=fill, neginf=fill)


def _read_col(df: pd.DataFrame, required: str, aliases: Sequence[str] = ()) -> pd.Series:
    idx = {c.strip().lower(): c for c in df.columns}
    names = (required, *aliases)
    for n in names:
        key = n.strip().lower()
        if key in idx:
            return df[idx[key]]
    raise ValueError(f"Kolom wajib tidak ditemukan: {names}")


def _clean_aggregated_df(df: pd.DataFrame) -> pd.DataFrame:
    # 1) warm-up / cooldown trimming
    if len(df) <= (WARMUP_ROWS + COOLDOWN_ROWS):
        # Sengaja dibuat kosong agar caller memberi error yang jelas.
        return df.iloc[0:0].copy()
    df = df.iloc[WARMUP_ROWS:-COOLDOWN_ROWS].copy()

    # 2) keep productive windows only
    total_requests = pd.to_numeric(_read_col(df, "total_requests"), errors="coerce").fillna(0)
    df = df[total_requests > 0].copy()

    # 3) forward fill RT zero -> previous value (only in middle of data)
    rt_col_name = _read_col(df, "node1_response_ms", aliases=("node1_latency_ms",)).name
    rt = pd.to_numeric(df[rt_col_name], errors="coerce")
    rt = rt.mask(rt <= 0, np.nan).ffill()
    rt = rt.fillna(rt.median() if rt.notna().any() else 1.0)
    df[rt_col_name] = rt

    return df.reset_index(drop=True)


def load_offline_samples_csv(csv_path: str | Path) -> OfflineDataset:
    csv_path = Path(csv_path)
    df_raw = pd.read_csv(csv_path, sep=None, engine="python", on_bad_lines="warn")
    if df_raw.empty:
        raise ValueError(f"Dataset kosong: {csv_path}")

    df = _clean_aggregated_df(df_raw)
    if df.empty:
        raise ValueError(f"Dataset habis setelah cleaning: {csv_path}")

    ts_col = None
    for c in ("timestamp_utc", "timestamp"):
        try:
            ts_col = _read_col(df, c)
            break
        except Exception:
            pass
    if ts_col is None:
        ts = np.array([""] * len(df), dtype=object)
    else:
        ts = ts_col.astype(str).fillna("").to_numpy()

    cpu = _sanitize_float_array(pd.to_numeric(_read_col(df, "node1_cpu_normalized_usage", aliases=("node1_cpu_raw_usage",)), errors="coerce").to_numpy(), 0.0)
    rt = _sanitize_float_array(pd.to_numeric(_read_col(df, "node1_response_ms", aliases=("node1_latency_ms",)), errors="coerce").to_numpy(), 1.0)
    q = _sanitize_float_array(pd.to_numeric(_read_col(df, "node1_inflight", aliases=("node1_queue",)), errors="coerce").to_numpy(), 0.0)
    rps = _sanitize_float_array(pd.to_numeric(_read_col(df, "total_requests"), errors="coerce").to_numpy(), 0.0)

    scenario = csv_path.stem
    return OfflineDataset(
        scenario_name=scenario,
        timestamp=ts,
        node1_cpu_norm=np.clip(cpu, 0.0, 100.0),
        node1_response_ms=np.maximum(rt, 0.001),
        node1_inflight=np.maximum(q, 0.0),
        total_requests=np.maximum(rps, 0.0),
    )


def lower_bound(_d: int) -> float:
    return 0.0


def upper_bound(d: int) -> float:
    if d <= 8:
        return 100.0
    return 1000.0


def repair_params(params: np.ndarray) -> np.ndarray:
    p = np.asarray(params, dtype=np.float64).copy()
    if p.shape[0] != DIMENSIONS:
        raise ValueError(f"Panjang params harus {DIMENSIONS}, dapat {p.shape[0]}")

    for i in range(0, p.shape[0] - 2, 3):
        min_gap = 2.0 if i <= 8 else 20.0
        lo = lower_bound(i)
        hi = upper_bound(i)

        a = float(np.clip(p[i], lo, hi))
        b = float(np.clip(p[i + 1], lo, hi))
        c = float(np.clip(p[i + 2], lo, hi))

        if a > b:
            a, b = b, a
        if b > c:
            b, c = c, b
        if a > b:
            a, b = b, a

        if b < a + min_gap:
            b = a + min_gap
        if c < b + min_gap:
            c = b + min_gap

        if c > hi:
            c = hi
            if b > c - min_gap:
                b = c - min_gap
            if b < a + min_gap:
                a = max(lo, b - min_gap)

        p[i], p[i + 1], p[i + 2] = a, b, c

    for base in (0, 9, 18):
        peaks = np.array([p[base + 1], p[base + 4], p[base + 7]], dtype=np.float64)
        peaks.sort()
        gap = 2.0 if base == 0 else 20.0
        hi = 100.0 if base == 0 else 1000.0
        if peaks[1] < peaks[0] + gap:
            peaks[1] = peaks[0] + gap
        if peaks[2] < peaks[1] + gap:
            peaks[2] = peaks[1] + gap
        if peaks[2] > hi:
            peaks[2] = hi
            peaks[1] = min(peaks[1], peaks[2] - gap)
            peaks[0] = min(peaks[0], peaks[1] - gap)
        p[base + 1], p[base + 4], p[base + 7] = peaks

    return p


def fuzzify_left(v: np.ndarray, _a: float, b: float, c: float) -> np.ndarray:
    out = np.ones_like(v, dtype=np.float64)
    out = np.where(v >= c, 0.0, out)
    den = c - b
    if den <= 0:
        return np.where(v <= b, 1.0, 0.0)
    mid = (v > b) & (v < c)
    out[mid] = (c - v[mid]) / den
    return out


def fuzzify_triangle(v: np.ndarray, a: float, b: float, c: float) -> np.ndarray:
    return fuzz.trimf(v, [a, b, c]).astype(np.float64)


def fuzzify_right(v: np.ndarray, a: float, b: float, _c: float) -> np.ndarray:
    out = np.ones_like(v, dtype=np.float64)
    out = np.where(v <= a, 0.0, out)
    den = b - a
    if den <= 0:
        return np.where(v >= b, 1.0, 0.0)
    mid = (v > a) & (v < b)
    out[mid] = (v[mid] - a) / den
    return out


def fuzzy_score_vectorized(params: np.ndarray, cpu: np.ndarray, q: np.ndarray, rt: np.ndarray) -> np.ndarray:
    p = params
    mu_cpu = (
        fuzzify_left(cpu, p[0], p[1], p[2]),
        fuzzify_triangle(cpu, p[3], p[4], p[5]),
        fuzzify_right(cpu, p[6], p[7], p[8]),
    )
    mu_q = (
        fuzzify_left(q, p[9], p[10], p[11]),
        fuzzify_triangle(q, p[12], p[13], p[14]),
        fuzzify_right(q, p[15], p[16], p[17]),
    )
    mu_r = (
        fuzzify_left(rt, p[18], p[19], p[20]),
        fuzzify_triangle(rt, p[21], p[22], p[23]),
        fuzzify_right(rt, p[24], p[25], p[26]),
    )

    n = cpu.shape[0]
    alpha_out = np.zeros((3, n), dtype=np.float64)
    for cpu_idx, q_idx, r_idx, out_idx in COMPILED_RULES:
        a = np.minimum(np.minimum(mu_cpu[cpu_idx], mu_q[q_idx]), mu_r[r_idx])
        alpha_out[out_idx] = np.maximum(alpha_out[out_idx], a)

    a_total = np.zeros(n, dtype=np.float64)
    m_total = np.zeros(n, dtype=np.float64)
    for out_idx in range(3):
        alpha = alpha_out[out_idx]
        tri = OUT_MF[out_idx]
        area = alpha * (tri[2] - tri[0]) / 2.0
        moment = area * (tri[0] + tri[1] + tri[2]) / 3.0
        a_total += area
        m_total += moment

    score = np.zeros(n, dtype=np.float64)
    nz = a_total > 0
    score[nz] = m_total[nz] / a_total[nz]
    return np.clip(score, 0.0, 100.0)


def _normalized(v: np.ndarray) -> np.ndarray:
    v = np.asarray(v, dtype=np.float64)
    # Robust scaling (percentile) agar tidak terlalu sensitif pada outlier / noise kecil.
    lo = float(np.nanpercentile(v, 5))
    hi = float(np.nanpercentile(v, 95))
    den = max(hi - lo, 1e-9)
    return np.clip((v - lo) / den, 0.0, 1.0)


def _param_ranges() -> np.ndarray:
    # CPU MF: 0..100 (9 dim). Queue/RT MF: 0..1000 (18 dim).
    return np.array([100.0] * 9 + [1000.0] * 18, dtype=np.float64)


def evaluate_objective(params: np.ndarray, ds: OfflineDataset, base_params: Optional[np.ndarray] = None) -> OfflineObjective:
    p = repair_params(params)
    base = repair_params(base_params) if base_params is not None else None

    cpu = ds.node1_cpu_norm
    rt = ds.node1_response_ms
    q = ds.node1_inflight
    rps = ds.total_requests

    score = fuzzy_score_vectorized(p, cpu, q, rt)

    # Target skor ideal berbasis intensitas beban agregat (throughput + RT + inflight).
    thr_n = _normalized(rps)
    rt_n = _normalized(rt)
    q_n = _normalized(q)
    stress = 0.45 * thr_n + 0.30 * rt_n + 0.25 * q_n
    target_score = 100.0 * (1.0 - stress)

    # Obj-1: seberapa dekat fuzzy score terhadap target kondisi beban.
    weights = np.maximum(rps, 1.0)
    mse = float(np.average((score - target_score) ** 2, weights=weights))

    # Obj-2: estimasi RT setelah aksi routing berdasarkan score (score tinggi -> penurunan RT).
    rt_sim = rt * (1.0 - 0.35 * (score / 100.0))
    weighted_rt = float(np.sum(rt_sim * rps) / max(np.sum(rps), 1.0))

    # Regularisasi: tetap dekat dengan base params untuk mengurangi overfitting pada noise kecil.
    if base is not None:
        ranges = _param_ranges()
        reg = float(np.mean(((p - base) / ranges) ** 2))
        mse += PARAM_REG_WEIGHT * reg

    return OfflineObjective(load_alignment_mse=mse, weighted_sim_rt=weighted_rt)


def dominates(a: OfflineObjective, b: OfflineObjective) -> bool:
    be = (a.load_alignment_mse <= b.load_alignment_mse) and (a.weighted_sim_rt <= b.weighted_sim_rt)
    sb = (a.load_alignment_mse < b.load_alignment_mse) or (a.weighted_sim_rt < b.weighted_sim_rt)
    return bool(be and sb)


def objective_score(o: OfflineObjective) -> float:
    return o.load_alignment_mse + (o.weighted_sim_rt / 2000.0)


def add_to_archive(archive: List[OfflineSolution], candidate: OfflineSolution) -> List[OfflineSolution]:
    keep: List[OfflineSolution] = []
    for i, sol in enumerate(archive):
        if dominates(sol.objective, candidate.objective):
            keep.extend(archive[i:])
            return keep
        if not dominates(candidate.objective, sol.objective):
            keep.append(sol)

    keep.append(candidate)
    if len(keep) <= MAX_ARCHIVE:
        return keep
    keep.sort(key=lambda s: (s.objective.load_alignment_mse, s.objective.weighted_sim_rt))
    return keep[:MAX_ARCHIVE]


def optimize_offline(ds: OfflineDataset, base_params: np.ndarray, cfg: OfflineConfig) -> Dict[str, object]:
    cfg = cfg.normalized()
    base = repair_params(base_params)

    rng = np.random.default_rng(cfg.seed)

    x = np.empty((cfg.particles, DIMENSIONS), dtype=np.float64)
    v = np.empty((cfg.particles, DIMENSIONS), dtype=np.float64)
    pbest = np.empty((cfg.particles, DIMENSIONS), dtype=np.float64)
    pbest_obj: List[Optional[OfflineObjective]] = [None] * cfg.particles

    for i in range(cfg.particles):
        for d in range(DIMENSIONS):
            x[i, d] = float(np.clip(base[d] + rng.uniform(-cfg.initial_spread, cfg.initial_spread), lower_bound(d), upper_bound(d)))
            v[i, d] = float(rng.uniform(-1.0, 1.0))
        x[i] = repair_params(x[i])
        pbest[i] = x[i].copy()

    archive: List[OfflineSolution] = []

    for t in range(cfg.iterations):
        w = INERTIA_MAX_W - (INERTIA_MAX_W - INERTIA_MIN_W) * (float(t) / float(max(cfg.iterations - 1, 1)))

        for i in range(cfg.particles):
            obj = evaluate_objective(x[i], ds, base_params=base)
            if pbest_obj[i] is None:
                pbest_obj[i] = obj
                pbest[i] = x[i].copy()
            else:
                old = pbest_obj[i]
                assert old is not None
                if dominates(obj, old) or ((not dominates(old, obj)) and (objective_score(obj) < objective_score(old))):
                    pbest_obj[i] = obj
                    pbest[i] = x[i].copy()

            archive = add_to_archive(archive, OfflineSolution(params=x[i].tolist(), objective=obj))

        if not archive:
            continue

        for i in range(cfg.particles):
            leader = archive[int(rng.integers(0, len(archive)))]
            leader_params = np.asarray(leader.params, dtype=np.float64)

            for d in range(DIMENSIONS):
                r1 = float(rng.uniform(0.0, 1.0))
                r2 = float(rng.uniform(0.0, 1.0))
                vel = (
                    w * v[i, d]
                    + COGNITIVE_C1 * r1 * (pbest[i, d] - x[i, d])
                    + SOCIAL_C2 * r2 * (leader_params[d] - x[i, d])
                )
                vel = float(np.clip(vel, -VELOCITY_CLAMP, VELOCITY_CLAMP))
                nxt = x[i, d] + vel
                v[i, d] = vel
                x[i, d] = float(np.clip(nxt, lower_bound(d), upper_bound(d)))

            x[i] = repair_params(x[i])

    if not archive:
        raise RuntimeError("Pareto archive kosong")

    archive.sort(key=lambda s: (s.objective.load_alignment_mse, s.objective.weighted_sim_rt))
    best = min(archive, key=lambda s: objective_score(s.objective))

    return {
        "best": best,
        "archive": archive,
        "config": asdict(cfg),
    }


def load_base_params(base_json_path: Optional[str | Path] = None) -> np.ndarray:
    if base_json_path is None:
        return DEFAULT_BASE_PARAMS.copy()
    p = Path(base_json_path)
    if not p.exists():
        return DEFAULT_BASE_PARAMS.copy()
    arr = np.asarray(json.loads(p.read_text(encoding="utf-8")), dtype=np.float64)
    if arr.shape[0] != DIMENSIONS:
        raise ValueError(f"panjang base params harus {DIMENSIONS}, dapat {arr.shape[0]}")
    return arr


def to_go_header(params: Sequence[float], var_name: str = "OptimizedFuzzyParams") -> str:
    lines = [f"var {var_name} = []float64{{"]
    for i in range(0, len(params), 9):
        chunk = params[i : i + 9]
        lines.append("    " + ", ".join(f"{float(v):.6f}" for v in chunk) + ",")
    lines.append("}")
    return "\n".join(lines)


def to_go_file(params: Sequence[float], package_name: str = "config", var_name: str = "OptimizedFuzzyParams") -> str:
    header = [f"package {package_name}", "", "// Code-generated by offline_trace_mopso_art_colab.py", ""]
    return "\n".join(header) + to_go_header(params=params, var_name=var_name) + "\n"


def run_scenario(
    csv_path: str | Path,
    out_dir: str | Path,
    base_params_path: Optional[str | Path] = None,
    lb_out_params_path: Optional[str | Path] = None,
    lb_go_package: str = "config",
    particles: int = NUM_PARTICLES_DEFAULT,
    iterations: int = ITERATIONS_DEFAULT,
    spread: float = 3.0,
    seed: int = 0,
) -> Dict[str, object]:
    ds = load_offline_samples_csv(csv_path)
    base = load_base_params(base_params_path)

    result = optimize_offline(
        ds,
        base,
        OfflineConfig(
            particles=particles,
            iterations=iterations,
            initial_spread=spread,
            seed=seed,
        ),
    )

    best: OfflineSolution = result["best"]

    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    scenario = ds.scenario_name
    params_out = out_dir / f"opt_fuzzy_{scenario}.json"
    go_out = out_dir / f"opt_fuzzy_{scenario}.go.txt"
    report_out = out_dir / f"mopso_report_{scenario}.json"

    params_out.write_text(json.dumps(best.params, indent=2, ensure_ascii=False), encoding="utf-8")
    go_out.write_text(to_go_header(best.params), encoding="utf-8")

    # Output tambahan yang "langsung pakai" untuk Load Balancer Go (default path-nya: storage/optimized_fuzzy_params.json)
    lb_params_out = None
    lb_go_out = None
    if lb_out_params_path is not None:
        lb_params_out = Path(lb_out_params_path)
        lb_params_out.parent.mkdir(parents=True, exist_ok=True)
        lb_params_out.write_text(json.dumps(best.params, indent=2, ensure_ascii=False), encoding="utf-8")
        lb_go_out = lb_params_out.with_suffix(".go")
        lb_go_out.write_text(to_go_file(best.params, package_name=lb_go_package), encoding="utf-8")

    report = {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "scenario": scenario,
        "input_csv": str(csv_path),
        "dataset_summary": {
            "sample_count": ds.sample_count,
            "feature_columns": [
                "node1_cpu_normalized_usage",
                "node1_response_ms",
                "node1_inflight",
                "total_requests",
            ],
            "cleaning_rules": {
                "trim_warmup": WARMUP_ROWS,
                "trim_cooldown": COOLDOWN_ROWS,
                "filter_total_requests_gt_0": True,
                "forward_fill_rt_zero": True,
            },
        },
        "config": result["config"],
        "training_tweaks": {
            "robust_percentile_scaling": {"p_lo": 5, "p_hi": 95},
            "param_regularization_weight": PARAM_REG_WEIGHT,
        },
        "best": {
            "params": best.params,
            "objective": asdict(best.objective),
            "score": objective_score(best.objective),
        },
        "archive_size": len(result["archive"]),
        "outputs": {
            "params_json": str(params_out),
            "go_header": str(go_out),
            "lb_params_json": str(lb_params_out) if lb_params_out is not None else None,
            "lb_go_file": str(lb_go_out) if lb_go_out is not None else None,
        },
    }
    report_out.write_text(json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")

    return {
        "scenario": scenario,
        "params_json": str(params_out),
        "go_header": str(go_out),
        "report": str(report_out),
        "lb_params_json": str(lb_params_out) if lb_params_out is not None else None,
        "lb_go_file": str(lb_go_out) if lb_go_out is not None else None,
    }


def run_batch(
    csv_files: Sequence[str | Path],
    out_dir: str | Path,
    base_params_path: Optional[str | Path] = None,
    lb_out_params_path: Optional[str | Path] = None,
    lb_go_package: str = "config",
    particles: int = NUM_PARTICLES_DEFAULT,
    iterations: int = ITERATIONS_DEFAULT,
    spread: float = 3.0,
    seed: int = 0,
) -> List[Dict[str, object]]:
    if lb_out_params_path is not None and len(csv_files) > 1:
        raise ValueError("--lb-out-params hanya aman untuk 1 CSV (agar tidak overwrite). Jalankan per-skenario.")
    outputs: List[Dict[str, object]] = []
    for csv_path in csv_files:
        outputs.append(
            run_scenario(
                csv_path=csv_path,
                out_dir=out_dir,
                base_params_path=base_params_path,
                lb_out_params_path=lb_out_params_path,
                lb_go_package=lb_go_package,
                particles=particles,
                iterations=iterations,
                spread=spread,
                seed=seed,
            )
        )
    return outputs


if __name__ == "__main__":
    import argparse

    parser = argparse.ArgumentParser(description="Offline MOPSO + Fuzzy training untuk dataset agregasi per 1 detik.")
    parser.add_argument("--csv", action="append", default=[], help="Path CSV dataset (boleh diulang untuk batch).")
    parser.add_argument("--out-dir", default="/content/mopso_outputs", help="Folder output (default: /content/mopso_outputs).")
    parser.add_argument("--base-params", default=None, help="Opsional: path JSON base params (27 angka).")
    parser.add_argument(
        "--lb-out-params",
        default=None,
        help="Opsional: path output JSON yang langsung dipakai Go LB (contoh: load-balancer/storage/optimized_fuzzy_params.json).",
    )
    parser.add_argument("--lb-go-package", default="config", help="Nama package untuk file Go yang digenerate (default: config).")
    parser.add_argument("--particles", type=int, default=NUM_PARTICLES_DEFAULT, help="Jumlah partikel (default: tuned untuk agregasi).")
    parser.add_argument("--iterations", type=int, default=ITERATIONS_DEFAULT, help="Jumlah iterasi (default: tuned untuk agregasi).")
    parser.add_argument("--spread", type=float, default=3.0, help="Jitter awal dari base params (default: 3.0).")
    parser.add_argument("--seed", type=int, default=0, help="Seed random (0 = otomatis).")
    args = parser.parse_args()

    # Fallback gaya Colab lama: edit manual daftar CSV_FILES jika tidak pakai argumen.
    CSV_FILES = args.csv or [
        # "/content/fix_hasil_terbaru.csv",
    ]

    if not CSV_FILES:
        print("Berikan minimal 1 CSV via --csv (atau isi manual CSV_FILES di __main__).")
        raise SystemExit(2)

    res = run_batch(
        csv_files=CSV_FILES,
        out_dir=args.out_dir,
        base_params_path=args.base_params,
        lb_out_params_path=args.lb_out_params,
        lb_go_package=args.lb_go_package,
        particles=args.particles,
        iterations=args.iterations,
        spread=args.spread,
        seed=args.seed,
    )
    print(json.dumps(res, indent=2, ensure_ascii=False))
