"""
Google Colab-ready script: Offline Trace-Driven Optimization (MOPSO + Fuzzy)
Translasi dari Go:
- load-balancer/pkg/mopso/offline_training.go
- load-balancer/pkg/mopso/mopso.go
- load-balancer/cmd/mopso-train/main.go (loader CSV + best-run selection)

Fitur utama:
1) Isolated per-skenario: tiap CSV di-train independen dari nol.
2) Kapasitas dinamis: baca node*_cpu_capacity dari CSV, tanpa hardcode kapasitas.
3) Fitness 1:1: evaluateDatasetDIAndBCU ditranslasikan matematis.
4) Output JSON per skenario: opt_fuzzy_<scenario>.json.

Dependency (Colab):
!pip -q install pandas numpy scikit-fuzzy
"""

from __future__ import annotations

import json
import math
import time
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Tuple

import numpy as np
import pandas as pd
import skfuzzy as fuzz


# ----------------------------
# Konstanta dari implementasi Go
# ----------------------------

DIMENSIONS = 27
NUM_PARTICLES_DEFAULT = 20
ITERATIONS_DEFAULT = 1900
MAX_ARCHIVE = 128
INERTIA_MAX_W = 0.50
INERTIA_MIN_W = 0.40
COGNITIVE_C1 = 1.0
SOCIAL_C2 = 2.0

# Rule base terkompilasi (identik dengan compiledRules di Go)
# tuple: (cpu_idx, queue_idx, resp_idx, out_idx)
COMPILED_RULES: Tuple[Tuple[int, int, int, int], ...] = (
    (0, 0, 0, 2), (0, 0, 1, 2), (0, 0, 2, 1), (0, 1, 0, 2), (0, 1, 1, 1), (0, 1, 2, 1), (0, 2, 0, 1), (0, 2, 1, 1), (0, 2, 2, 0),
    (1, 0, 0, 2), (1, 0, 1, 1), (1, 0, 2, 1), (1, 1, 0, 1), (1, 1, 1, 1), (1, 1, 2, 0), (1, 2, 0, 1), (1, 2, 1, 0), (1, 2, 2, 0),
    (2, 0, 0, 1), (2, 0, 1, 1), (2, 0, 2, 0), (2, 1, 0, 1), (2, 1, 1, 0), (2, 1, 2, 0), (2, 2, 0, 0), (2, 2, 1, 0), (2, 2, 2, 0),
)

# Output MF triangles: index 0=Rendah,1=Sedang,2=Tinggi
OUT_MF = np.array([[0.0, 25.0, 50.0], [25.0, 50.0, 75.0], [50.0, 75.0, 100.0]], dtype=np.float64)

DEFAULT_BASE_PARAMS = np.array(
    [
        0, 0, 50, 0, 50, 100, 50, 100, 100,
        0, 0, 50, 0, 50, 100, 50, 100, 100,
        0, 0, 500, 0, 500, 1000, 500, 1000, 1000,
    ],
    dtype=np.float64,
)


# ----------------------------
# Data model
# ----------------------------

@dataclass
class OfflineDataset:
    scenario_name: str
    timestamp: np.ndarray
    os_idle_cpu: np.ndarray
    node1_requests: np.ndarray
    node2_requests: np.ndarray
    node1_cpu_raw: np.ndarray
    node2_cpu_raw: np.ndarray
    node1_cpu_capacity: np.ndarray
    node2_cpu_capacity: np.ndarray
    node1_queue: np.ndarray
    node2_queue: np.ndarray
    node1_response_ms: np.ndarray
    node2_response_ms: np.ndarray

    @property
    def sample_count(self) -> int:
        return int(self.node1_requests.shape[0])

    @property
    def usable_mask(self) -> np.ndarray:
        return (self.node1_requests + self.node2_requests) > 0

    @property
    def used_samples(self) -> int:
        return int(np.count_nonzero(self.usable_mask))


@dataclass
class OfflineObjective:
    di: float
    bcu: float

    @property
    def peak_load(self) -> float:
        return 1.0 - self.bcu

    @property
    def balanced_score(self) -> float:
        return self.di + (1.0 - self.bcu)


@dataclass
class OfflineSolution:
    params: List[float]
    objective: OfflineObjective


@dataclass
class OfflineConfig:
    particles: int = NUM_PARTICLES_DEFAULT
    iterations: int = ITERATIONS_DEFAULT
    initial_spread: float = 8.0
    seed: int = 0

    def normalized(self) -> "OfflineConfig":
        particles = self.particles if self.particles > 0 else NUM_PARTICLES_DEFAULT
        iterations = self.iterations if self.iterations > 0 else ITERATIONS_DEFAULT
        spread = self.initial_spread if self.initial_spread > 0 else 8.0
        seed = self.seed if self.seed != 0 else int(time.time_ns())
        return OfflineConfig(particles=particles, iterations=iterations, initial_spread=spread, seed=seed)


@dataclass
class OfflineResult:
    generated_at: str
    sample_count: int
    used_samples: int
    config: OfflineConfig
    archive: List[OfflineSolution]
    best_balanced: OfflineSolution
    best_di: OfflineSolution
    best_bcu: OfflineSolution


# ----------------------------
# Helper util
# ----------------------------

def clamp(v: np.ndarray | float, lo: float, hi: float):
    return np.clip(v, lo, hi)


def lower_bound(_d: int) -> float:
    return 0.0


def upper_bound(d: int) -> float:
    if d <= 8:
        return 100.0
    if d <= 17:
        return 100.0
    return 1000.0


def max_int(a: int, b: int) -> int:
    return a if a > b else b


def max_int64_array(x: np.ndarray, minimum: int = 1) -> np.ndarray:
    return np.maximum(x, minimum)


def to_raw_cpu(cpu: np.ndarray, capacity: np.ndarray) -> np.ndarray:
    cap = np.where(capacity <= 0, 100.0, capacity)
    return np.clip(cpu, 0.0, cap)


def to_normalized_cpu(cpu: np.ndarray, capacity: np.ndarray) -> np.ndarray:
    cap = np.where(capacity <= 0, 100.0, capacity)
    raw = to_raw_cpu(cpu, cap)
    norm = (raw / cap) * 100.0
    return np.clip(norm, 0.0, 100.0)


def objective_score(obj: OfflineObjective) -> float:
    return obj.di + (1.0 - obj.bcu)


def dominates(a: OfflineObjective, b: OfflineObjective) -> bool:
    # minimization untuk (DI, PeakLoad=1-BCU)
    better_or_equal = (a.di <= b.di) and (a.peak_load <= b.peak_load)
    strictly_better = (a.di < b.di) or (a.peak_load < b.peak_load)
    return bool(better_or_equal and strictly_better)


def clone_params(params: np.ndarray) -> np.ndarray:
    out = np.asarray(params, dtype=np.float64).copy()
    if out.shape[0] != DIMENSIONS:
        raise ValueError(f"Panjang parameter harus {DIMENSIONS}, dapat {out.shape[0]}")
    return out


# ----------------------------
# Param repair (1:1 dari Go)
# ----------------------------

def enforce_peak_order(params: np.ndarray, start: int, min_gap: float, hi: float) -> None:
    peaks = np.array([params[start + 1], params[start + 4], params[start + 7]], dtype=np.float64)
    peaks.sort()

    if peaks[1] < peaks[0] + min_gap:
        peaks[1] = peaks[0] + min_gap
    if peaks[2] < peaks[1] + min_gap:
        peaks[2] = peaks[1] + min_gap

    if peaks[2] > hi:
        peaks[2] = hi
        if peaks[1] > peaks[2] - min_gap:
            peaks[1] = peaks[2] - min_gap
        if peaks[0] > peaks[1] - min_gap:
            peaks[0] = peaks[1] - min_gap

    if peaks[0] < 0:
        peaks[0] = 0

    params[start + 1] = peaks[0]
    params[start + 4] = peaks[1]
    params[start + 7] = peaks[2]

    # Pastikan triangle tetap mengandung puncak dengan lebar minimum.
    for tri in range(3):
        a_idx = start + tri * 3
        b_idx = a_idx + 1
        c_idx = a_idx + 2

        b = params[b_idx]
        lo = lower_bound(a_idx)
        hi_tri = upper_bound(a_idx)

        if params[a_idx] > b - min_gap:
            params[a_idx] = b - min_gap
        if params[c_idx] < b + min_gap:
            params[c_idx] = b + min_gap

        params[a_idx] = float(np.clip(params[a_idx], lo, hi_tri))
        params[c_idx] = float(np.clip(params[c_idx], lo, hi_tri))

        if params[a_idx] > b:
            params[a_idx] = float(np.clip(b - min_gap, lo, hi_tri))
        if params[c_idx] < b:
            params[c_idx] = float(np.clip(b + min_gap, lo, hi_tri))


def repair_params(params: np.ndarray) -> np.ndarray:
    p = clone_params(params)

    for i in range(0, p.shape[0] - 2, 3):
        min_gap = 2.0 if i <= 17 else 20.0

        a = float(np.clip(p[i], lower_bound(i), upper_bound(i)))
        b = float(np.clip(p[i + 1], lower_bound(i + 1), upper_bound(i + 1)))
        c = float(np.clip(p[i + 2], lower_bound(i + 2), upper_bound(i + 2)))

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

        hi = upper_bound(i)
        lo = lower_bound(i)

        if c > hi:
            c = hi
            if b > c - min_gap:
                b = c - min_gap
            if b < a + min_gap:
                a = b - min_gap

        if a < lo:
            a = lo
        if b < a + min_gap:
            b = a + min_gap
        if c < b + min_gap:
            c = b + min_gap
        if c > hi:
            c = hi

        p[i], p[i + 1], p[i + 2] = a, b, c

    enforce_peak_order(p, 0, 2.0, 100.0)
    enforce_peak_order(p, 9, 5.0, 100.0)
    enforce_peak_order(p, 18, 20.0, 1000.0)

    # Overlap enforcement per variabel (3 segitiga per variabel)
    for i in range(0, p.shape[0] - 8, 9):
        if p[i + 2] < p[i + 3]:
            mid = (p[i + 2] + p[i + 3]) / 2.0
            p[i + 2] = mid
            p[i + 3] = mid
        if p[i + 5] < p[i + 6]:
            mid = (p[i + 5] + p[i + 6]) / 2.0
            p[i + 5] = mid
            p[i + 6] = mid

    return p


def is_sane_params(params: np.ndarray) -> bool:
    if params.shape[0] != DIMENSIONS:
        return False

    min_cpu_width = 2.0
    min_qr_width = 20.0

    for i in range(0, params.shape[0] - 2, 3):
        a, b, c = params[i], params[i + 1], params[i + 2]
        if not (a <= b <= c):
            return False
        min_w = min_cpu_width if i <= 6 else min_qr_width
        if (b - a) < min_w or (c - b) < min_w:
            return False

    for idx in ((1, 4, 7), (10, 13, 16), (19, 22, 25)):
        if not (params[idx[0]] < params[idx[1]] < params[idx[2]]):
            return False

    return True


# ----------------------------
# Fuzzy engine (vectorized)
# ----------------------------

def fuzzify_left(v: np.ndarray, _a: float, b: float, c: float) -> np.ndarray:
    # Identik dengan Go: <=b => 1, >=c => 0, linear di antaranya.
    out = np.ones_like(v, dtype=np.float64)
    out = np.where(v >= c, 0.0, out)
    den = c - b
    if den <= 0:
        return np.where(v <= b, 1.0, 0.0)
    mid = (v > b) & (v < c)
    out[mid] = (c - v[mid]) / den
    return out


def fuzzify_triangle(v: np.ndarray, a: float, b: float, c: float) -> np.ndarray:
    # Pakai scikit-fuzzy untuk MF segitiga (sesuai requirement dependency).
    # Formula trimf setara dengan implementasi Go untuk triangle biasa.
    return fuzz.trimf(v, [a, b, c]).astype(np.float64)


def fuzzify_right(v: np.ndarray, a: float, b: float, _c: float) -> np.ndarray:
    # Identik dengan Go: >=b => 1, <=a => 0, linear di antaranya.
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
    non_zero = a_total > 0
    score[non_zero] = m_total[non_zero] / a_total[non_zero]
    return score


# ----------------------------
# Fitness evaluator (1:1 Go evaluateDatasetDIAndBCU)
# ----------------------------

def evaluate_dataset_di_bcu(params: np.ndarray, ds: OfflineDataset) -> OfflineObjective:
    params = repair_params(params)

    req_total = ds.node1_requests + ds.node2_requests
    usable = req_total > 0

    if not np.any(usable):
        return OfflineObjective(di=1.0, bcu=0.0)

    cap1 = np.where(ds.node1_cpu_capacity <= 0, 100.0, ds.node1_cpu_capacity)
    cap2 = np.where(ds.node2_cpu_capacity <= 0, 100.0, ds.node2_cpu_capacity)

    cpu1_raw = to_raw_cpu(ds.node1_cpu_raw, cap1)
    cpu2_raw = to_raw_cpu(ds.node2_cpu_raw, cap2)
    cpu1_fuzzy = to_normalized_cpu(ds.node1_cpu_raw, cap1)
    cpu2_fuzzy = to_normalized_cpu(ds.node2_cpu_raw, cap2)

    score1 = fuzzy_score_vectorized(params, cpu1_fuzzy, ds.node1_queue, ds.node1_response_ms)
    score2 = fuzzy_score_vectorized(params, cpu2_fuzzy, ds.node2_queue, ds.node2_response_ms)

    total_score = score1 + score2
    share1 = np.full_like(total_score, 0.5, dtype=np.float64)
    positive = total_score > 0
    share1[positive] = np.clip(score1[positive] / total_score[positive], 0.0, 1.0)

    pred_req1 = req_total.astype(np.float64) * share1
    pred_req2 = req_total.astype(np.float64) - pred_req1

    os_idle = np.where(ds.os_idle_cpu <= 0, 10.0, ds.os_idle_cpu)

    obs_req1 = max_int64_array(ds.node1_requests, 1).astype(np.float64)
    obs_req2 = max_int64_array(ds.node2_requests, 1).astype(np.float64)

    cost_per_req1 = (cpu1_raw - os_idle) / obs_req1
    cost_per_req2 = (cpu2_raw - os_idle) / obs_req2
    cost_per_req1 = np.maximum(cost_per_req1, 0.0)
    cost_per_req2 = np.maximum(cost_per_req2, 0.0)

    sim_cpu1 = os_idle + (cost_per_req1 * pred_req1)
    sim_cpu2 = os_idle + (cost_per_req2 * pred_req2)

    sim_cpu1 = np.maximum(sim_cpu1, 0.0)
    sim_cpu2 = np.maximum(sim_cpu2, 0.0)

    sim_cpu1 = np.minimum(sim_cpu1, cap1)
    sim_cpu2 = np.minimum(sim_cpu2, cap2)

    norm_cpu1 = (sim_cpu1 / cap1) * 100.0
    norm_cpu2 = (sim_cpu2 / cap2) * 100.0
    norm_cpu1 = np.clip(norm_cpu1, 0.0, 100.0)
    norm_cpu2 = np.clip(norm_cpu2, 0.0, 100.0)

    norm_cpu1 = norm_cpu1[usable]
    norm_cpu2 = norm_cpu2[usable]

    avg_cpu1 = float(np.mean(norm_cpu1))
    avg_cpu2 = float(np.mean(norm_cpu2))
    avg_total = (avg_cpu1 + avg_cpu2) / 2.0

    if avg_total > 0:
        hi = max(avg_cpu1, avg_cpu2)
        lo = min(avg_cpu1, avg_cpu2)
        di = (hi - lo) / avg_total
    else:
        di = 0.0

    if avg_total < 1.0:
        bcu = 1.0
    else:
        dev1 = abs(avg_cpu1 - avg_total)
        dev2 = abs(avg_cpu2 - avg_total)
        bcu = 1.0 - ((dev1 + dev2) / (2.0 * avg_total))

    bcu = float(np.clip(bcu, 0.0, 1.0))
    return OfflineObjective(di=float(di), bcu=bcu)


# ----------------------------
# CSV Loader (translasi loadOfflineSamples)
# ----------------------------

def _find_header(index: Dict[str, str], *aliases: str) -> Optional[str]:
    for a in aliases:
        k = a.strip().lower()
        if k in index:
            return index[k]
    return None


def _require_header(index: Dict[str, str], *aliases: str) -> str:
    found = _find_header(index, *aliases)
    if found is None:
        raise ValueError(f"Kolom wajib tidak ditemukan. Alias dicari: {aliases}")
    return found


def _to_numeric_strict(df: pd.DataFrame, col: str, kind: str = "float") -> np.ndarray:
    try:
        series = pd.to_numeric(df[col], errors="raise")
    except Exception as exc:
        raise ValueError(f"Gagal parse numeric kolom '{col}': {exc}") from exc

    if kind == "int":
        return series.astype("int64").to_numpy()
    return series.astype("float64").to_numpy()


def load_offline_samples_csv(csv_path: str | Path) -> OfflineDataset:
    csv_path = Path(csv_path)
    df = pd.read_csv(csv_path)
    if df.empty:
        raise ValueError(f"Dataset kosong: {csv_path}")

    # map lowercase->original column name
    col_index = {c.strip().lower(): c for c in df.columns}

    req1_col = _require_header(col_index, "node1_requests")
    req2_col = _require_header(col_index, "node2_requests")

    cpu1_col = _find_header(col_index, "node1_cpu_raw_usage", "node1_cpu_usage_raw")
    cpu2_col = _find_header(col_index, "node2_cpu_raw_usage", "node2_cpu_usage_raw")
    if cpu1_col is None or cpu2_col is None:
        raise ValueError("Dataset tidak valid: butuh kolom CPU raw node1/node2")

    cap1_col = _require_header(col_index, "node1_cpu_capacity")
    cap2_col = _require_header(col_index, "node2_cpu_capacity")
    q1_col = _require_header(col_index, "node1_queue")
    q2_col = _require_header(col_index, "node2_queue")
    r1_col = _require_header(col_index, "node1_response_ms", "node1_latency_ms")
    r2_col = _require_header(col_index, "node2_response_ms", "node2_latency_ms")

    os_idle_col = _find_header(col_index, "os_idle_cpu")
    ts_col = _find_header(col_index, "timestamp_utc", "timestamp")
    cpu_unit_col = _find_header(col_index, "cpu_usage_unit")

    if cpu_unit_col is not None:
        unit_values = df[cpu_unit_col].astype(str).str.strip().str.lower()
        invalid = unit_values[(unit_values != "") & (unit_values != "raw_percent_of_host")]
        if not invalid.empty:
            sample_bad = invalid.iloc[0]
            raise ValueError(
                f"cpu_usage_unit={sample_bad!r} tidak didukung (harus raw_percent_of_host)"
            )

    req1 = _to_numeric_strict(df, req1_col, "int")
    req2 = _to_numeric_strict(df, req2_col, "int")
    cpu1 = _to_numeric_strict(df, cpu1_col, "float")
    cpu2 = _to_numeric_strict(df, cpu2_col, "float")
    cap1 = _to_numeric_strict(df, cap1_col, "float")
    cap2 = _to_numeric_strict(df, cap2_col, "float")
    q1 = _to_numeric_strict(df, q1_col, "float")
    q2 = _to_numeric_strict(df, q2_col, "float")
    r1 = _to_numeric_strict(df, r1_col, "float")
    r2 = _to_numeric_strict(df, r2_col, "float")

    if os_idle_col is not None:
        os_idle = pd.to_numeric(df[os_idle_col], errors="coerce").fillna(10.0).astype("float64").to_numpy()
    else:
        os_idle = np.full(req1.shape[0], 10.0, dtype=np.float64)

    if ts_col is not None:
        ts = df[ts_col].astype(str).fillna("").to_numpy()
    else:
        ts = np.array([""] * req1.shape[0], dtype=object)

    if req1.shape[0] == 0:
        raise ValueError("dataset tidak memiliki row data")

    scenario = csv_path.stem
    if scenario.startswith("trace_"):
        scenario = scenario.replace("trace_", "", 1)

    return OfflineDataset(
        scenario_name=scenario,
        timestamp=ts,
        os_idle_cpu=os_idle,
        node1_requests=req1,
        node2_requests=req2,
        node1_cpu_raw=cpu1,
        node2_cpu_raw=cpu2,
        node1_cpu_capacity=cap1,
        node2_cpu_capacity=cap2,
        node1_queue=q1,
        node2_queue=q2,
        node1_response_ms=r1,
        node2_response_ms=r2,
    )


def load_base_params(base_json_path: Optional[str | Path] = None) -> np.ndarray:
    if base_json_path is None:
        return DEFAULT_BASE_PARAMS.copy()

    p = Path(base_json_path)
    if not p.exists():
        return DEFAULT_BASE_PARAMS.copy()

    with p.open("r", encoding="utf-8") as f:
        arr = json.load(f)

    arr_np = np.asarray(arr, dtype=np.float64)
    if arr_np.shape[0] != DIMENSIONS:
        raise ValueError(f"panjang base params harus {DIMENSIONS}, dapat {arr_np.shape[0]}")
    return arr_np


# ----------------------------
# MOPSO Offline (translasi OptimizeOffline)
# ----------------------------

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

    keep.sort(key=lambda s: (s.objective.peak_load, s.objective.di))
    return keep[:MAX_ARCHIVE]


def choose_sane_candidate(result: OfflineResult) -> Optional[OfflineSolution]:
    best = result.best_balanced
    if is_sane_params(np.asarray(best.params, dtype=np.float64)):
        return best

    for sol in result.archive:
        if is_sane_params(np.asarray(sol.params, dtype=np.float64)):
            return sol
    return None


def optimize_offline(ds: OfflineDataset, base_params: np.ndarray, cfg: OfflineConfig) -> OfflineResult:
    cfg = cfg.normalized()
    base_params = repair_params(base_params)

    if base_params.shape[0] != DIMENSIONS:
        raise ValueError("base params harus berukuran 27")
    if ds.sample_count == 0:
        raise ValueError("dataset kosong")
    if ds.used_samples == 0:
        raise ValueError("dataset tidak punya sample dengan total request > 0")

    rng = np.random.default_rng(cfg.seed)

    # init particle swarm
    x = np.empty((cfg.particles, DIMENSIONS), dtype=np.float64)
    v = np.empty((cfg.particles, DIMENSIONS), dtype=np.float64)
    pbest = np.empty((cfg.particles, DIMENSIONS), dtype=np.float64)
    pbest_obj: List[Optional[OfflineObjective]] = [None] * cfg.particles

    for i in range(cfg.particles):
        for d in range(DIMENSIONS):
            base = base_params[d]
            delta = cfg.initial_spread
            x[i, d] = float(np.clip(base + rng.uniform(-delta, delta), lower_bound(d), upper_bound(d)))
            v[i, d] = float(rng.uniform(-1.0, 1.0))
        x[i] = repair_params(x[i])
        pbest[i] = x[i].copy()

    archive: List[OfflineSolution] = []

    for t in range(cfg.iterations):
        w = INERTIA_MAX_W - (INERTIA_MAX_W - INERTIA_MIN_W) * (float(t) / float(max_int(cfg.iterations - 1, 1)))

        for i in range(cfg.particles):
            obj = evaluate_dataset_di_bcu(x[i], ds)

            if pbest_obj[i] is None:
                pbest_obj[i] = obj
                pbest[i] = x[i].copy()
            else:
                old = pbest_obj[i]
                assert old is not None
                if dominates(obj, old) or ((not dominates(old, obj)) and (objective_score(obj) < objective_score(old))):
                    pbest_obj[i] = obj
                    pbest[i] = x[i].copy()

            cand = OfflineSolution(params=x[i].tolist(), objective=obj)
            archive = add_to_archive(archive, cand)

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
                nxt = x[i, d] + vel
                v[i, d] = vel
                x[i, d] = float(np.clip(nxt, lower_bound(d), upper_bound(d)))

            x[i] = repair_params(x[i])

    if not archive:
        raise RuntimeError("pareto archive kosong")

    archive.sort(key=lambda s: s.objective.balanced_score)

    best_balanced = archive[0]
    best_di = min(archive, key=lambda s: s.objective.di)
    best_bcu = max(archive, key=lambda s: s.objective.bcu)

    return OfflineResult(
        generated_at=time.strftime("%Y-%m-%dT%H:%M:%S"),
        sample_count=ds.sample_count,
        used_samples=ds.used_samples,
        config=cfg,
        archive=archive,
        best_balanced=best_balanced,
        best_di=best_di,
        best_bcu=best_bcu,
    )


# ----------------------------
# Batch per skenario (isolated)
# ----------------------------

def run_scenario_optimization(
    csv_path: str | Path,
    base_params: np.ndarray,
    out_dir: str | Path,
    particles: int = 30,
    iterations: int = 3000,
    spread: float = 8.0,
    seed: int = 0,
    runs: int = 5,
    allow_regression: bool = False,
) -> Dict:
    ds = load_offline_samples_csv(csv_path)

    total_runs = runs if runs > 0 else 1
    base_seed = seed if seed != 0 else int(time.time_ns())

    summaries: List[Dict] = []
    best_result: Optional[OfflineResult] = None
    best_score = float("inf")
    best_run_meta: Optional[Dict] = None

    for i in range(total_runs):
        run_seed = base_seed + i
        cfg = OfflineConfig(particles=particles, iterations=iterations, initial_spread=spread, seed=run_seed)

        try:
            result = optimize_offline(ds, base_params, cfg)
            score = result.best_balanced.objective.balanced_score
            summaries.append(
                {
                    "run_index": i + 1,
                    "seed": int(run_seed),
                    "score_balanced": float(score),
                    "error": None,
                }
            )

            if best_result is None or score < best_score:
                best_result = result
                best_score = score
                best_run_meta = summaries[-1]

        except Exception as exc:
            summaries.append(
                {
                    "run_index": i + 1,
                    "seed": int(run_seed),
                    "score_balanced": float("inf"),
                    "error": str(exc),
                }
            )

    if best_result is None:
        raise RuntimeError(f"Semua run gagal untuk skenario {ds.scenario_name}")

    sane_candidate = choose_sane_candidate(best_result)
    if sane_candidate is None:
        repaired = repair_params(np.asarray(best_result.best_balanced.params, dtype=np.float64))
        if not is_sane_params(repaired):
            raise RuntimeError("Tidak ada kandidat sane di semua run")
        best_result.best_balanced = OfflineSolution(params=repaired.tolist(), objective=best_result.best_balanced.objective)
    else:
        best_result.best_balanced = sane_candidate

    # evaluasi base vs optimized
    base_obj = evaluate_dataset_di_bcu(base_params, ds)
    opt_obj = evaluate_dataset_di_bcu(np.asarray(best_result.best_balanced.params, dtype=np.float64), ds)
    best_result.best_balanced.objective = opt_obj

    base_score = base_obj.balanced_score
    opt_score = opt_obj.balanced_score

    if (not allow_regression) and (opt_score >= base_score):
        raise RuntimeError(
            f"Optimized tidak mengalahkan base pada skenario {ds.scenario_name} "
            f"(base={base_score:.6f}, opt={opt_score:.6f})"
        )

    scenario_name = ds.scenario_name
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    params_out = out_dir / f"opt_fuzzy_{scenario_name}.json"
    report_out = out_dir / f"mopso_report_{scenario_name}.json"

    with params_out.open("w", encoding="utf-8") as f:
        json.dump(best_result.best_balanced.params, f, ensure_ascii=False, indent=2)

    archive_export = [
        {
            "params": sol.params,
            "objective": {"di": sol.objective.di, "bcu": sol.objective.bcu},
            "score_balanced": sol.objective.balanced_score,
        }
        for sol in best_result.archive
    ]

    payload = {
        "scenario": scenario_name,
        "input_csv": str(csv_path),
        "selected_run": best_run_meta,
        "runs": summaries,
        "base_objective": {"di": base_obj.di, "bcu": base_obj.bcu, "score_balanced": base_score},
        "optimized_objective": {"di": opt_obj.di, "bcu": opt_obj.bcu, "score_balanced": opt_score},
        "result": {
            "generated_at": best_result.generated_at,
            "sample_count": best_result.sample_count,
            "used_samples": best_result.used_samples,
            "config": asdict(best_result.config),
            "best_balanced": {
                "params": best_result.best_balanced.params,
                "objective": {
                    "di": best_result.best_balanced.objective.di,
                    "bcu": best_result.best_balanced.objective.bcu,
                    "score_balanced": best_result.best_balanced.objective.balanced_score,
                },
            },
            "best_di": {
                "params": best_result.best_di.params,
                "objective": {
                    "di": best_result.best_di.objective.di,
                    "bcu": best_result.best_di.objective.bcu,
                    "score_balanced": best_result.best_di.objective.balanced_score,
                },
            },
            "best_bcu": {
                "params": best_result.best_bcu.params,
                "objective": {
                    "di": best_result.best_bcu.objective.di,
                    "bcu": best_result.best_bcu.objective.bcu,
                    "score_balanced": best_result.best_bcu.objective.balanced_score,
                },
            },
            "archive": archive_export,
        },
    }

    with report_out.open("w", encoding="utf-8") as f:
        json.dump(payload, f, ensure_ascii=False, indent=2)

    return {
        "scenario": scenario_name,
        "params_out": str(params_out),
        "report_out": str(report_out),
        "sample_count": ds.sample_count,
        "used_samples": ds.used_samples,
        "base_score": base_score,
        "opt_score": opt_score,
    }


def run_batch_scenarios(
    csv_files: Sequence[str | Path],
    base_params_path: Optional[str | Path] = None,
    out_dir: str | Path = "./outputs",
    particles: int = 30,
    iterations: int = 3000,
    spread: float = 8.0,
    seed: int = 0,
    runs: int = 5,
    allow_regression: bool = False,
) -> List[Dict]:
    base_params = load_base_params(base_params_path)
    base_params = repair_params(base_params)

    outputs: List[Dict] = []
    for csv_file in csv_files:
        summary = run_scenario_optimization(
            csv_path=csv_file,
            base_params=base_params,
            out_dir=out_dir,
            particles=particles,
            iterations=iterations,
            spread=spread,
            seed=seed,
            runs=runs,
            allow_regression=allow_regression,
        )
        outputs.append(summary)
    return outputs


# ----------------------------
# Contoh pemakaian di Colab
# ----------------------------
if __name__ == "__main__":
    # 1) Isi daftar CSV skenario (isolated, tidak digabung)
    CSV_FILES = [
        # "/content/trace_normal.csv",
        # "/content/trace_spike.csv",
        # "/content/trace_ramp.csv",
    ]

    # 2) Opsional: base params JSON dari Go
    BASE_PARAMS_JSON = None  # contoh: "/content/base_fuzzy_params.json"

    # 3) Jalankan batch
    if not CSV_FILES:
        print("Isi dulu CSV_FILES di bagian __main__")
    else:
        result = run_batch_scenarios(
            csv_files=CSV_FILES,
            base_params_path=BASE_PARAMS_JSON,
            out_dir="/content/mopso_outputs",
            particles=30,
            iterations=3000,
            spread=8.0,
            seed=0,
            runs=5,
            allow_regression=False,
        )
        print(json.dumps(result, indent=2, ensure_ascii=False))
