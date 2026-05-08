"""
Google Colab-ready script: Offline Trace-Driven Optimization (MOPSO + Fuzzy)
Translasi dari Go:
- load-balancer/pkg/mopso/offline_training.go
- load-balancer/pkg/mopso/mopso.go
- load-balancer/cmd/mopso-train/main.go (loader CSV + best-run selection)

Fitur utama:
1) Isolated per-skenario: tiap CSV di-train independen dari nol.
2) Kapasitas dinamis: baca node*_cpu_capacity dari CSV, tanpa hardcode kapasitas.
3) Fitness 1:1: evaluasi DI + ART ditranslasikan matematis.
4) Output JSON + trace visual per skenario.

Dependency (Colab):
!pip -q install pandas numpy scikit-fuzzy
"""

from __future__ import annotations

import json
import math
import os
import time
from dataclasses import dataclass, asdict, field
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Tuple

import matplotlib.pyplot as plt
import numpy as np
import pandas as pd

os.environ.setdefault("MPLCONFIGDIR", "/tmp")

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

try:
    from tqdm.auto import tqdm
except Exception:
    def tqdm(iterable=None, **kwargs):
        if iterable is None:
            class _NullTqdm(object):
                def update(self, _n=1):
                    return None

                def set_postfix(self, **_kwargs):
                    return None

                def close(self):
                    return None

            return _NullTqdm()
        return iterable


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
        0, 40, 75, 60, 80, 95, 85, 95, 100,
        0, 50, 150, 100, 250, 400, 300, 500, 1000,
        0, 150, 300, 200, 500, 800, 600, 850, 1000,
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
    art: float


@dataclass
class ArchiveScoreRow:
    index: int
    params: List[float]
    di: float
    art: float
    norm_di: float
    norm_art: float
    score: float


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
    best_art: OfflineSolution
    best_balanced_score: float
    archive_trace: List[ArchiveScoreRow] = field(default_factory=list)
    normalization_stats: Dict[str, float] = field(default_factory=dict)
    score_history: List[float] = field(default_factory=list)


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
        return 1000.0
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
    # Tie-break internal saat update pBest; final selection memakai normalisasi archive.
    return obj.di + (obj.art / 1000.0)


def dominates(a: OfflineObjective, b: OfflineObjective) -> bool:
    # Pareto murni: minimisasi DI dan ART.
    better_or_equal = (a.di <= b.di) and (a.art <= b.art)
    strictly_better = (a.di < b.di) or (a.art < b.art)
    return bool(better_or_equal and strictly_better)


def minmax_scale(values: np.ndarray) -> Tuple[np.ndarray, float, float]:
    values = np.asarray(values, dtype=np.float64)
    if values.size == 0:
        return values, 0.0, 0.0
    min_v = float(np.min(values))
    max_v = float(np.max(values))
    den = max(max_v - min_v, 1e-9)
    scaled = (values - min_v) / den
    return scaled, min_v, max_v


def minmax_scale_value(value: float, min_v: float, max_v: float) -> float:
    return float((float(value) - float(min_v)) / max(max_v - min_v, 1e-9))


def balanced_score_from_normalized(norm_di: float, norm_art: float) -> float:
    return (0.5 * norm_di) + (0.5 * norm_art)


def archive_trace_rows(archive: Sequence[OfflineSolution]) -> tuple[list[ArchiveScoreRow], Dict[str, float]]:
    if len(archive) == 0:
        return [], {}

    di_values = np.array([sol.objective.di for sol in archive], dtype=np.float64)
    art_values = np.array([sol.objective.art for sol in archive], dtype=np.float64)
    norm_di, min_di, max_di = minmax_scale(di_values)
    norm_art, min_art, max_art = minmax_scale(art_values)
    scores = balanced_score_from_normalized(norm_di, norm_art)

    rows = [
        ArchiveScoreRow(
            index=i,
            params=clone_params(np.asarray(sol.params, dtype=np.float64)).tolist(),
            di=float(sol.objective.di),
            art=float(sol.objective.art),
            norm_di=float(norm_di[i]),
            norm_art=float(norm_art[i]),
            score=float(scores[i]),
        )
        for i, sol in enumerate(archive)
    ]
    stats = {
        "min_di": min_di,
        "max_di": max_di,
        "min_art": min_art,
        "max_art": max_art,
    }
    return rows, stats


def select_best_balanced_solution(archive: Sequence[OfflineSolution]) -> tuple[OfflineSolution, float, list[ArchiveScoreRow], Dict[str, float]]:
    rows, stats = archive_trace_rows(archive)
    if len(rows) == 0:
        raise ValueError("pareto archive kosong")
    best_row = min(rows, key=lambda row: (row.score, row.di, row.art))
    best = archive[best_row.index]
    stats["best_index"] = float(best_row.index)
    stats["best_score"] = float(best_row.score)
    return best, float(best_row.score), rows, stats


def find_trace_row(rows: Sequence[ArchiveScoreRow], params: Sequence[float]) -> Optional[ArchiveScoreRow]:
    target = np.asarray(params, dtype=np.float64)
    for row in rows:
        if np.allclose(np.asarray(row.params, dtype=np.float64), target, atol=1e-9, rtol=1e-9):
            return row
    return None


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
        min_gap = 2.0 if i <= 8 else 20.0

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
    enforce_peak_order(p, 9, 20.0, 1000.0)
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
# Fitness evaluator (1:1, DI + ART)
# ----------------------------

def evaluate_dataset_di_art(params: np.ndarray, ds: OfflineDataset) -> OfflineObjective:
    params = repair_params(params)

    req_total = ds.node1_requests + ds.node2_requests
    usable = req_total > 0

    if not np.any(usable):
        return OfflineObjective(di=1.0, art=0.0)

    cap1 = np.where(ds.node1_cpu_capacity <= 0, 100.0, ds.node1_cpu_capacity)
    cap2 = np.where(ds.node2_cpu_capacity <= 0, 100.0, ds.node2_cpu_capacity)
    rt1 = _sanitize_float_array(ds.node1_response_ms, _fallback_from_series(pd.Series(ds.node1_response_ms), 1.0))
    rt2 = _sanitize_float_array(ds.node2_response_ms, _fallback_from_series(pd.Series(ds.node2_response_ms), 1.0))

    cpu1_raw = to_raw_cpu(ds.node1_cpu_raw, cap1)
    cpu2_raw = to_raw_cpu(ds.node2_cpu_raw, cap2)
    cpu1_fuzzy = to_normalized_cpu(ds.node1_cpu_raw, cap1)
    cpu2_fuzzy = to_normalized_cpu(ds.node2_cpu_raw, cap2)

    score1 = fuzzy_score_vectorized(params, cpu1_fuzzy, ds.node1_queue, rt1)
    score2 = fuzzy_score_vectorized(params, cpu2_fuzzy, ds.node2_queue, rt2)

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

    art_num = float(np.sum((pred_req1[usable] * rt1[usable]) + (pred_req2[usable] * rt2[usable])))
    art_den = float(np.sum(req_total[usable]))
    art = art_num / art_den if art_den > 0 else 0.0

    return OfflineObjective(di=float(di), art=float(art))


def build_objective_validation_trace(params: np.ndarray, ds: OfflineDataset, max_rows: int = 5) -> Dict[str, object]:
    params = repair_params(params)

    req_total = ds.node1_requests + ds.node2_requests
    usable = req_total > 0
    cap1 = np.where(ds.node1_cpu_capacity <= 0, 100.0, ds.node1_cpu_capacity)
    cap2 = np.where(ds.node2_cpu_capacity <= 0, 100.0, ds.node2_cpu_capacity)
    rt1 = _sanitize_float_array(ds.node1_response_ms, _fallback_from_series(pd.Series(ds.node1_response_ms), 1.0))
    rt2 = _sanitize_float_array(ds.node2_response_ms, _fallback_from_series(pd.Series(ds.node2_response_ms), 1.0))

    cpu1_raw = to_raw_cpu(ds.node1_cpu_raw, cap1)
    cpu2_raw = to_raw_cpu(ds.node2_cpu_raw, cap2)
    cpu1_fuzzy = to_normalized_cpu(ds.node1_cpu_raw, cap1)
    cpu2_fuzzy = to_normalized_cpu(ds.node2_cpu_raw, cap2)

    score1 = fuzzy_score_vectorized(params, cpu1_fuzzy, ds.node1_queue, rt1)
    score2 = fuzzy_score_vectorized(params, cpu2_fuzzy, ds.node2_queue, rt2)
    total_score = score1 + score2
    share1 = np.full_like(total_score, 0.5, dtype=np.float64)
    positive = total_score > 0
    share1[positive] = np.clip(score1[positive] / total_score[positive], 0.0, 1.0)
    share2 = 1.0 - share1

    pred_req1 = req_total.astype(np.float64) * share1
    pred_req2 = req_total.astype(np.float64) - pred_req1

    os_idle = np.where(ds.os_idle_cpu <= 0, 10.0, ds.os_idle_cpu)
    obs_req1 = max_int64_array(ds.node1_requests, 1).astype(np.float64)
    obs_req2 = max_int64_array(ds.node2_requests, 1).astype(np.float64)
    cost_per_req1 = np.maximum((cpu1_raw - os_idle) / obs_req1, 0.0)
    cost_per_req2 = np.maximum((cpu2_raw - os_idle) / obs_req2, 0.0)

    sim_cpu1 = np.minimum(np.maximum(os_idle + (cost_per_req1 * pred_req1), 0.0), cap1)
    sim_cpu2 = np.minimum(np.maximum(os_idle + (cost_per_req2 * pred_req2), 0.0), cap2)

    norm_cpu1 = np.clip((sim_cpu1 / cap1) * 100.0, 0.0, 100.0)
    norm_cpu2 = np.clip((sim_cpu2 / cap2) * 100.0, 0.0, 100.0)
    norm_cpu1 = norm_cpu1[usable]
    norm_cpu2 = norm_cpu2[usable]

    avg_cpu1 = float(np.mean(norm_cpu1)) if norm_cpu1.size else 0.0
    avg_cpu2 = float(np.mean(norm_cpu2)) if norm_cpu2.size else 0.0
    avg_total = (avg_cpu1 + avg_cpu2) / 2.0
    if avg_total > 0:
        di = (max(avg_cpu1, avg_cpu2) - min(avg_cpu1, avg_cpu2)) / avg_total
    else:
        di = 0.0

    art_row = np.zeros_like(req_total, dtype=np.float64)
    art_row[usable] = (
        (pred_req1[usable] * rt1[usable])
        + (pred_req2[usable] * rt2[usable])
    ) / np.maximum(req_total[usable].astype(np.float64), 1e-9)
    art_num = float(np.sum((pred_req1[usable] * rt1[usable]) + (pred_req2[usable] * rt2[usable])))
    art_den = float(np.sum(req_total[usable]))
    art = art_num / art_den if art_den > 0 else 0.0

    usable_idx = np.flatnonzero(usable)
    preview_idx = usable_idx[:max_rows]
    rows = []
    for pos, i in enumerate(preview_idx):
        rows.append(
            {
                "row": int(i),
                "req_total": float(req_total[i]),
                "share1": float(share1[i]),
                "share2": float(share2[i]),
                "node1_rt_ms": float(rt1[i]),
                "node2_rt_ms": float(rt2[i]),
                "weighted_art_ms": float(art_row[i]),
                "cpu1_norm": float(norm_cpu1[pos]),
                "cpu2_norm": float(norm_cpu2[pos]),
            }
        )

    return {
        "avg_cpu1": avg_cpu1,
        "avg_cpu2": avg_cpu2,
        "avg_total_cpu": avg_total,
        "di": float(di),
        "art_numerator": art_num,
        "art_denominator": art_den,
        "art": float(art),
        "sample_preview": rows,
    }


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


def _sanitize_float_array(values: np.ndarray, fallback: float = 0.0) -> np.ndarray:
    arr = np.asarray(values, dtype=np.float64)
    if np.all(np.isfinite(arr)):
        return arr
    finite = arr[np.isfinite(arr)]
    fill = float(np.median(finite)) if finite.size > 0 else float(fallback)
    return np.nan_to_num(arr, nan=fill, posinf=fill, neginf=fill)


def _fallback_from_series(series: pd.Series, default: float = 0.0) -> float:
    finite = pd.to_numeric(series, errors="coerce").replace([np.inf, -np.inf], np.nan).dropna()
    if finite.empty:
        return float(default)
    return float(finite.median())


def load_offline_samples_csv(csv_path: str | Path) -> OfflineDataset:
    csv_path = Path(csv_path)
    df = pd.read_csv(csv_path, sep=None, engine="python", on_bad_lines="warn")
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

    cpu1 = _sanitize_float_array(pd.to_numeric(df[cpu1_col], errors="coerce").to_numpy(), 0.0)
    cpu2 = _sanitize_float_array(pd.to_numeric(df[cpu2_col], errors="coerce").to_numpy(), 0.0)
    cap1 = _sanitize_float_array(pd.to_numeric(df[cap1_col], errors="coerce").to_numpy(), 100.0)
    cap2 = _sanitize_float_array(pd.to_numeric(df[cap2_col], errors="coerce").to_numpy(), 100.0)
    q1 = _sanitize_float_array(pd.to_numeric(df[q1_col], errors="coerce").to_numpy(), 0.0)
    q2 = _sanitize_float_array(pd.to_numeric(df[q2_col], errors="coerce").to_numpy(), 0.0)
    r1 = _sanitize_float_array(pd.to_numeric(df[r1_col], errors="coerce").to_numpy(), _fallback_from_series(df[r1_col], 1.0))
    r2 = _sanitize_float_array(pd.to_numeric(df[r2_col], errors="coerce").to_numpy(), _fallback_from_series(df[r2_col], 1.0))

    if os_idle_col is not None:
        os_idle = _sanitize_float_array(pd.to_numeric(df[os_idle_col], errors="coerce").to_numpy(), 10.0)
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


def build_dataset_quality_report(ds: OfflineDataset) -> Dict[str, object]:
    def stats(arr: np.ndarray) -> Dict[str, float]:
        arr = np.asarray(arr, dtype=np.float64)
        finite = arr[np.isfinite(arr)]
        if finite.size == 0:
            return {"finite_count": 0.0, "nan_count": float(arr.size), "min": float("nan"), "max": float("nan"), "mean": float("nan")}
        return {
            "finite_count": float(finite.size),
            "nan_count": float(arr.size - finite.size),
            "min": float(np.min(finite)),
            "max": float(np.max(finite)),
            "mean": float(np.mean(finite)),
        }

    return {
        "scenario": ds.scenario_name,
        "sample_count": ds.sample_count,
        "used_samples": ds.used_samples,
        "node1_response_ms": stats(ds.node1_response_ms),
        "node2_response_ms": stats(ds.node2_response_ms),
        "node1_cpu_raw": stats(ds.node1_cpu_raw),
        "node2_cpu_raw": stats(ds.node2_cpu_raw),
        "node1_requests": stats(ds.node1_requests.astype(np.float64)),
        "node2_requests": stats(ds.node2_requests.astype(np.float64)),
    }


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

    keep.sort(key=lambda s: (s.objective.art, s.objective.di))
    return keep[:MAX_ARCHIVE]


def choose_sane_candidate(result: OfflineResult) -> Optional[OfflineSolution]:
    best = result.best_balanced
    if is_sane_params(np.asarray(best.params, dtype=np.float64)):
        return best

    for sol in result.archive:
        if is_sane_params(np.asarray(sol.params, dtype=np.float64)):
            return sol
    return None


def plot_convergence(history: Sequence[float], scenario_name: str, out_dir: str | Path) -> Path:
    out_dir_path = Path(out_dir)
    out_dir_path.mkdir(parents=True, exist_ok=True)
    out_file = out_dir_path / f"convergence_trace_{scenario_name}.png"

    if len(history) == 0:
        return out_file

    x = np.arange(1, len(history) + 1)
    y = np.asarray(history, dtype=np.float64)
    y[~np.isfinite(y)] = np.nan

    fig, ax = plt.subplots(figsize=(10, 5))
    ax.plot(x, y, color="#1f77b4", linewidth=2.0, label="Best Normalized Score")
    ax.set_title(f"Convergence Trace - {scenario_name}")
    ax.set_xlabel("Iterasi")
    ax.set_ylabel("Weighted Normalized Score")
    ax.grid(True, linestyle="--", alpha=0.35)
    ax.legend()
    fig.tight_layout()
    fig.savefig(out_file, dpi=150)
    plt.close(fig)
    return out_file


def plot_pareto_archive(archive_rows: Sequence[ArchiveScoreRow], selected: Optional[ArchiveScoreRow], scenario_name: str, out_dir: str | Path) -> Path:
    out_dir_path = Path(out_dir)
    out_dir_path.mkdir(parents=True, exist_ok=True)
    out_file = out_dir_path / f"pareto_archive_{scenario_name}.png"
    if len(archive_rows) == 0:
        return out_file

    di = np.array([r.di for r in archive_rows], dtype=np.float64)
    art = np.array([r.art for r in archive_rows], dtype=np.float64)
    score = np.array([r.score for r in archive_rows], dtype=np.float64)

    fig, ax = plt.subplots(figsize=(10, 6))
    sc = ax.scatter(di, art, c=score, cmap="viridis", s=42, alpha=0.85, edgecolors="none")
    ax.set_title(f"Pareto Archive - {scenario_name}")
    ax.set_xlabel("DI")
    ax.set_ylabel("ART (ms)")
    ax.grid(True, linestyle="--", alpha=0.3)
    cbar = fig.colorbar(sc, ax=ax)
    cbar.set_label("Weighted Score")

    if selected is not None:
        ax.scatter([selected.di], [selected.art], s=180, marker="*", color="#d62728", label="Selected")
        ax.legend(loc="best")

    fig.tight_layout()
    fig.savefig(out_file, dpi=150)
    plt.close(fig)
    return out_file


def plot_archive_normalization(archive_rows: Sequence[ArchiveScoreRow], selected: Optional[ArchiveScoreRow], scenario_name: str, out_dir: str | Path, top_n: int = 10) -> Path:
    out_dir_path = Path(out_dir)
    out_dir_path.mkdir(parents=True, exist_ok=True)
    out_file = out_dir_path / f"normalized_archive_{scenario_name}.png"
    if len(archive_rows) == 0:
        return out_file

    ordered = sorted(archive_rows, key=lambda r: (r.score, r.di, r.art))[:top_n]
    labels = [f"S{r.index + 1}" for r in ordered]
    x = np.arange(len(ordered))
    width = 0.28

    fig, ax1 = plt.subplots(figsize=(12, 6))
    ax1.bar(x - width, [r.norm_di for r in ordered], width=width, label="Norm DI", color="#1f77b4")
    ax1.bar(x, [r.norm_art for r in ordered], width=width, label="Norm ART", color="#ff7f0e")
    ax1.bar(x + width, [r.score for r in ordered], width=width, label="Score", color="#2ca02c")
    ax1.set_xticks(x)
    ax1.set_xticklabels(labels)
    ax1.set_ylabel("Normalized Value")
    ax1.set_title(f"Top Archive Candidates - {scenario_name}")
    ax1.grid(True, axis="y", linestyle="--", alpha=0.3)

    y_min, y_max = 0.0, 1.05
    if selected is not None:
        ax1.axhline(selected.score, color="#d62728", linestyle=":", linewidth=1.5, label="Selected Score")
        y_min = min(y_min, selected.score - 0.05)
        y_max = max(y_max, selected.score + 0.05)

    ax1.set_ylim(y_min, y_max)

    ax1.legend(loc="upper left")

    fig.tight_layout()
    fig.savefig(out_file, dpi=150)
    plt.close(fig)
    return out_file


def plot_objective_comparison(base_obj: OfflineObjective, opt_obj: OfflineObjective, scenario_name: str, out_dir: str | Path) -> Path:
    out_dir_path = Path(out_dir)
    out_dir_path.mkdir(parents=True, exist_ok=True)
    out_file = out_dir_path / f"objective_comparison_{scenario_name}.png"

    fig, axes = plt.subplots(1, 2, figsize=(11, 5))
    panels = [
        (axes[0], "DI", base_obj.di, opt_obj.di, "#7f7f7f", "#1f77b4"),
        (axes[1], "ART (ms)", base_obj.art, opt_obj.art, "#7f7f7f", "#ff7f0e"),
    ]

    for ax, title, base_val, opt_val, c1, c2 in panels:
        bars = ax.bar(["Base", "Optimized"], [base_val, opt_val], color=[c1, c2], width=0.55)
        ax.set_title(title)
        ax.grid(True, axis="y", linestyle="--", alpha=0.3)
        for bar in bars:
            height = bar.get_height()
            ax.text(bar.get_x() + bar.get_width() / 2.0, height, f"{height:.3f}", ha="center", va="bottom", fontsize=9)

    fig.suptitle(f"Base vs Optimized - {scenario_name}")
    fig.tight_layout(rect=[0, 0, 1, 0.95])
    fig.savefig(out_file, dpi=150)
    plt.close(fig)
    return out_file


def plot_dataset_overview(ds: OfflineDataset, scenario_name: str, out_dir: str | Path) -> Path:
    out_dir_path = Path(out_dir)
    out_dir_path.mkdir(parents=True, exist_ok=True)
    out_file = out_dir_path / f"dataset_overview_{scenario_name}.png"

    fig, axes = plt.subplots(2, 2, figsize=(12, 8))
    axes = axes.ravel()

    axes[0].plot(ds.node1_response_ms[:250], color="#1f77b4", linewidth=1.5)
    axes[0].set_title("Node1 Response Time (sample)")
    axes[0].set_ylabel("ms")
    axes[0].grid(True, linestyle="--", alpha=0.25)

    axes[1].plot(ds.node2_response_ms[:250], color="#ff7f0e", linewidth=1.5)
    axes[1].set_title("Node2 Response Time (sample)")
    axes[1].set_ylabel("ms")
    axes[1].grid(True, linestyle="--", alpha=0.25)

    axes[2].hist(ds.node1_response_ms, bins=40, color="#1f77b4", alpha=0.8)
    axes[2].set_title("Node1 Response Distribution")
    axes[2].set_xlabel("ms")
    axes[2].set_ylabel("count")
    axes[2].grid(True, linestyle="--", alpha=0.25)

    axes[3].hist(ds.node2_response_ms, bins=40, color="#ff7f0e", alpha=0.8)
    axes[3].set_title("Node2 Response Distribution")
    axes[3].set_xlabel("ms")
    axes[3].set_ylabel("count")
    axes[3].grid(True, linestyle="--", alpha=0.25)

    fig.suptitle(f"Dataset Overview - {scenario_name}")
    fig.tight_layout(rect=[0, 0, 1, 0.96])
    fig.savefig(out_file, dpi=150)
    plt.close(fig)
    return out_file


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
    score_history: List[float] = []
    best_score_so_far = float("inf")

    iter_progress = tqdm(range(cfg.iterations), desc=f"Optimize[{ds.scenario_name}]", unit="iter", leave=False)
    for t in iter_progress:
        w = INERTIA_MAX_W - (INERTIA_MAX_W - INERTIA_MIN_W) * (float(t) / float(max_int(cfg.iterations - 1, 1)))

        for i in range(cfg.particles):
            obj = evaluate_dataset_di_art(x[i], ds)

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
            score_history.append(best_score_so_far)
            continue

        _, iter_best, _, _ = select_best_balanced_solution(archive)
        if iter_best < best_score_so_far:
            best_score_so_far = iter_best
        score_history.append(best_score_so_far)
        try:
            iter_progress.set_postfix(best=f"{best_score_so_far:.6f}", archive=len(archive))
        except Exception:
            pass

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

    archive.sort(key=lambda s: (s.objective.di, s.objective.art))

    best_balanced, best_balanced_score, archive_trace, normalization_stats = select_best_balanced_solution(archive)
    best_di = min(archive, key=lambda s: s.objective.di)
    best_art = min(archive, key=lambda s: s.objective.art)

    return OfflineResult(
        generated_at=time.strftime("%Y-%m-%dT%H:%M:%S"),
        sample_count=ds.sample_count,
        used_samples=ds.used_samples,
        config=cfg,
        archive=archive,
        best_balanced=best_balanced,
        best_di=best_di,
        best_art=best_art,
        best_balanced_score=best_balanced_score,
        archive_trace=archive_trace,
        normalization_stats=normalization_stats,
        score_history=score_history,
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

    run_progress = tqdm(range(total_runs), desc=f"Runs[{ds.scenario_name}]", unit="run", leave=False)
    for i in run_progress:
        run_seed = base_seed + i
        cfg = OfflineConfig(particles=particles, iterations=iterations, initial_spread=spread, seed=run_seed)

        try:
            result = optimize_offline(ds, base_params, cfg)
            score = result.best_balanced_score
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
            try:
                run_progress.set_postfix(best=f"{best_score:.6f}")
            except Exception:
                pass

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
    base_obj = evaluate_dataset_di_art(base_params, ds)
    opt_obj = evaluate_dataset_di_art(np.asarray(best_result.best_balanced.params, dtype=np.float64), ds)
    best_result.best_balanced.objective = opt_obj

    selected_row = find_trace_row(best_result.archive_trace, best_result.best_balanced.params)
    stats = best_result.normalization_stats
    if stats:
        base_norm_di = minmax_scale_value(base_obj.di, stats["min_di"], stats["max_di"])
        base_norm_art = minmax_scale_value(base_obj.art, stats["min_art"], stats["max_art"])
        base_score = balanced_score_from_normalized(base_norm_di, base_norm_art)
        opt_norm_di = minmax_scale_value(opt_obj.di, stats["min_di"], stats["max_di"])
        opt_norm_art = minmax_scale_value(opt_obj.art, stats["min_art"], stats["max_art"])
        opt_score_from_objective = balanced_score_from_normalized(opt_norm_di, opt_norm_art)
    else:
        base_score = 0.0
        opt_score_from_objective = 0.0
    opt_score = opt_score_from_objective
    if selected_row is None and stats:
        selected_row = ArchiveScoreRow(
            index=-1,
            params=best_result.best_balanced.params,
            di=float(opt_obj.di),
            art=float(opt_obj.art),
            norm_di=float(opt_norm_di),
            norm_art=float(opt_norm_art),
            score=float(opt_score),
        )
    best_result.best_balanced_score = opt_score
    if best_run_meta is not None:
        best_run_meta["score_balanced"] = float(opt_score)

    if (not allow_regression) and (opt_score >= base_score):
        raise RuntimeError(
            f"Optimized tidak mengalahkan base pada skenario {ds.scenario_name} "
            f"(base={base_score:.6f}, opt={opt_score:.6f})"
        )

    scenario_name = ds.scenario_name
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    convergence_out = plot_convergence(best_result.score_history, scenario_name, out_dir)
    pareto_out = plot_pareto_archive(best_result.archive_trace, selected_row, scenario_name, out_dir)
    normalized_out = plot_archive_normalization(best_result.archive_trace, selected_row, scenario_name, out_dir)
    comparison_out = plot_objective_comparison(base_obj, opt_obj, scenario_name, out_dir)

    params_out = out_dir / f"opt_fuzzy_{scenario_name}.json"
    report_out = out_dir / f"mopso_report_{scenario_name}.json"

    with params_out.open("w", encoding="utf-8") as f:
        json.dump(best_result.best_balanced.params, f, ensure_ascii=False, indent=2)

    trace_map = {tuple(row.params): row for row in best_result.archive_trace}
    archive_export = []
    for sol in best_result.archive:
        row = trace_map.get(tuple(sol.params))
        archive_export.append(
            {
                "params": sol.params,
                "objective": {"di": sol.objective.di, "art": sol.objective.art},
                "score_balanced": row.score if row is not None else None,
            }
        )

    validation_steps = {
        "step_1_dataset_summary": {
            "scenario": scenario_name,
            "sample_count": ds.sample_count,
            "used_samples": ds.used_samples,
            "archive_size": len(best_result.archive),
        },
        "step_2_objective_reference": {
            "di_formula": "abs(max(avg_cpu1, avg_cpu2) - min(avg_cpu1, avg_cpu2)) / ((avg_cpu1 + avg_cpu2)/2)",
            "art_formula": "(sum(pred_req1*rt1 + pred_req2*rt2) / sum(total_req))",
        },
        "step_3_base_trace": build_objective_validation_trace(base_params, ds),
        "step_4_optimized_trace": build_objective_validation_trace(np.asarray(best_result.best_balanced.params, dtype=np.float64), ds),
        "step_5_normalization": best_result.normalization_stats,
        "step_6_selected_solution": {
            "params": best_result.best_balanced.params,
            "di": float(best_result.best_balanced.objective.di),
            "art": float(best_result.best_balanced.objective.art),
            "score_balanced": float(opt_score),
            "pareto_row": asdict(selected_row) if selected_row is not None else None,
        },
    }

    payload = {
        "scenario": scenario_name,
        "input_csv": str(csv_path),
        "selected_run": best_run_meta,
        "runs": summaries,
        "base_objective": {"di": base_obj.di, "art": base_obj.art, "score_balanced": base_score},
        "optimized_objective": {"di": opt_obj.di, "art": opt_obj.art, "score_balanced": opt_score},
        "validation_steps": validation_steps,
        "result": {
            "generated_at": best_result.generated_at,
            "sample_count": best_result.sample_count,
            "used_samples": best_result.used_samples,
            "config": asdict(best_result.config),
            "best_balanced": {
                "params": best_result.best_balanced.params,
                "objective": {
                    "di": best_result.best_balanced.objective.di,
                    "art": best_result.best_balanced.objective.art,
                    "score_balanced": opt_score,
                },
            },
            "best_di": {
                "params": best_result.best_di.params,
                "objective": {
                    "di": best_result.best_di.objective.di,
                    "art": best_result.best_di.objective.art,
                },
            },
            "best_art": {
                "params": best_result.best_art.params,
                "objective": {
                    "di": best_result.best_art.objective.di,
                    "art": best_result.best_art.objective.art,
                },
            },
            "archive": archive_export,
            "archive_trace": [asdict(row) for row in best_result.archive_trace],
            "convergence_plot": str(convergence_out),
            "pareto_plot": str(pareto_out),
            "normalization_plot": str(normalized_out),
            "comparison_plot": str(comparison_out),
            "score_history": best_result.score_history,
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
        "convergence_plot": str(convergence_out),
        "pareto_plot": str(pareto_out),
        "normalization_plot": str(normalized_out),
        "comparison_plot": str(comparison_out),
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
    scenario_progress = tqdm(csv_files, desc="Scenarios", unit="csv", leave=False)
    for csv_file in scenario_progress:
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
