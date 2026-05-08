import argparse
import re
from collections import Counter
from datetime import datetime


AUDIT_PATTERN = re.compile(
    r"\[DECISION_AUDIT\]\s*\|\s*Q1:(-?\d+(?:\.\d+)?)\s*\|\s*Q2:(-?\d+(?:\.\d+)?)\s*\|\s*"
    r"S1:(-?\d+(?:\.\d+)?)\s*\|\s*S2:(-?\d+(?:\.\d+)?)\s*\|\s*R:(-?\d+(?:\.\d+)?)\s*\|\s*Win:([^\s|]+)"
)
LEGACY_PATTERN = re.compile(
    r"\[DECISION\].*?\[api-node1: CPU=(\d+(?:\.\d+)?)%.*?Lat=(\d+(?:\.\d+)?)ms.*?\]"
    r".*?\[api-node2: CPU=(\d+(?:\.\d+)?)%.*?Lat=(\d+(?:\.\d+)?)ms.*?\]"
)
GO_TS_PATTERN = re.compile(r"(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})")


def safe_mean(values):
    return sum(values) / len(values) if values else 0.0


def parse_go_timestamp(line):
    match = GO_TS_PATTERN.search(line)
    if not match:
        return None
    try:
        return datetime.strptime(match.group(1), "%Y/%m/%d %H:%M:%S")
    except ValueError:
        return None


def infer_win_side(winner):
    w = winner.lower()
    if "node1" in w:
        return 1
    if "node2" in w:
        return 2
    return 0


def evaluate_decision_audit(log_path):
    q1s, q2s = [], []
    s1s, s2s = [], []
    rs = []
    winners = []
    timestamps = []

    with open(log_path, "r", encoding="utf-8", errors="ignore") as f:
        for line in f:
            m = AUDIT_PATTERN.search(line)
            if not m:
                continue
            q1, q2, s1, s2, r, win = m.groups()
            q1s.append(float(q1))
            q2s.append(float(q2))
            s1s.append(float(s1))
            s2s.append(float(s2))
            rs.append(float(r))
            winners.append(win.strip())
            ts = parse_go_timestamp(line)
            if ts is not None:
                timestamps.append(ts)

    total = len(winners)
    if total == 0:
        return None

    total_scores = [a + b for a, b in zip(s1s, s2s)]
    nonzero_total = [x for x in total_scores if x > 0]
    p1_expected = [a / t for a, t in zip(s1s, total_scores) if t > 0]

    rr_valid = 0
    rr_match = 0
    rr_inferable = 0
    for s1, s2, r, win in zip(s1s, s2s, rs, winners):
        total_score = s1 + s2
        if total_score <= 0 or r < 0:
            continue
        if r <= total_score:
            rr_valid += 1
        predicted = 1 if r <= s1 else 2
        win_side = infer_win_side(win)
        if win_side > 0:
            rr_inferable += 1
            if predicted == win_side:
                rr_match += 1

    duration_sec = 0.0
    if len(timestamps) >= 2:
        duration_sec = (max(timestamps) - min(timestamps)).total_seconds()
    throughput = total / duration_sec if duration_sec > 0 else 0.0

    win_counts = Counter(winners)
    win1 = sum(v for k, v in win_counts.items() if infer_win_side(k) == 1)
    win2 = sum(v for k, v in win_counts.items() if infer_win_side(k) == 2)
    inferred_total = win1 + win2
    p1_actual = (win1 / inferred_total) if inferred_total > 0 else 0.0

    print("=" * 68)
    print(" LAPORAN EVALUASI LOG BARU (DECISION_AUDIT)")
    print("=" * 68)
    print("File log                : {}".format(log_path))
    print("Total keputusan         : {}".format(total))
    if duration_sec > 0:
        print("Makespan                : {:.2f} detik".format(duration_sec))
        print("Throughput keputusan    : {:.2f} keputusan/detik".format(throughput))
    else:
        print("Makespan                : tidak tersedia (timestamp tidak cukup)")
    print("-" * 68)
    print("METRIK INPUT FUZZY (DARI SNAPSHOT AUDIT):")
    print("Avg Q1 / Q2             : {:.2f} / {:.2f}".format(safe_mean(q1s), safe_mean(q2s)))
    print("Avg |Q1-Q2|             : {:.2f}".format(safe_mean([abs(a-b) for a, b in zip(q1s, q2s)])))
    print("Avg S1 / S2             : {:.4f} / {:.4f}".format(safe_mean(s1s), safe_mean(s2s)))
    print("Avg Total Score         : {:.4f}".format(safe_mean(nonzero_total)))
    print("-" * 68)
    print("METRIK ROULETTE:")
    print("R valid (0..S1+S2)      : {}/{}".format(rr_valid, total))
    if rr_inferable > 0:
        print("R match dengan Win      : {}/{} ({:.2f}%)".format(rr_match, rr_inferable, (rr_match / rr_inferable) * 100))
    else:
        print("R match dengan Win      : tidak inferable (nama node tidak standar)")
    print("Expected p(node1)       : {:.4f}".format(safe_mean(p1_expected)))
    if inferred_total > 0:
        print("Actual p(node1)         : {:.4f}".format(p1_actual))
    else:
        print("Actual p(node1)         : tidak inferable")
    print("-" * 68)
    print("DISTRIBUSI PEMENANG:")
    for name, cnt in win_counts.most_common():
        pct = (cnt / total) * 100.0
        print("  {:<24} {:>8} ({:>6.2f}%)".format(name, cnt, pct))
    print("=" * 68)
    return True


def evaluate_legacy_decision(log_path):
    node1_cpus, node2_cpus = [], []
    node1_lats, node2_lats = [], []

    with open(log_path, "r", encoding="utf-8", errors="ignore") as f:
        for line in f:
            m = LEGACY_PATTERN.search(line)
            if not m:
                continue
            cpu1, lat1, cpu2, lat2 = m.groups()
            node1_cpus.append(float(cpu1))
            node2_cpus.append(float(cpu2))
            node1_lats.append(float(lat1))
            node2_lats.append(float(lat2))

    total_data = len(node1_cpus)
    if total_data == 0:
        return None

    avg_cpu1 = safe_mean(node1_cpus)
    avg_cpu2 = safe_mean(node2_cpus)
    avg_total_cpu = (avg_cpu1 + avg_cpu2) / 2
    avg_lat1 = safe_mean(node1_lats)
    avg_lat2 = safe_mean(node2_lats)

    if avg_total_cpu < 1.0:
        bcu = 1.0
    else:
        dev_node1 = abs(avg_cpu1 - avg_total_cpu)
        dev_node2 = abs(avg_cpu2 - avg_total_cpu)
        bcu = 1 - ((dev_node1 + dev_node2) / (2 * avg_total_cpu))
    bcu = max(0.0, bcu)

    print("=" * 50)
    print(" LAPORAN EVALUASI LOAD BALANCER (LEGACY DECISION)")
    print("=" * 50)
    print("Total Sampel Keputusan : {} baris log".format(total_data))
    print("-" * 50)
    print("METRIK PER NODE (RATA-RATA):")
    print("   Node 1 -> CPU: {:>6.2f}% | Latency: {:>8.2f} ms".format(avg_cpu1, avg_lat1))
    print("   Node 2 -> CPU: {:>6.2f}% | Latency: {:>8.2f} ms".format(avg_cpu2, avg_lat2))
    print("-" * 50)
    print("METRIK SKRIPSI (EVALUASI KINERJA):")
    print("   Resource Utilization (RU) : {:.2f}%".format(avg_total_cpu))
    print("   Balanced CPU (BCU)        : {:.4f}".format(bcu))
    print("=" * 50)
    return True


def main():
    parser = argparse.ArgumentParser(
        description="Evaluator log load balancer (DECISION_AUDIT baru + fallback legacy)."
    )
    parser.add_argument(
        "log_file",
        nargs="?",
        default="load-balancer/storage/fbase.log",
        help="Path file log yang ingin dievaluasi",
    )
    args = parser.parse_args()

    try:
        if evaluate_decision_audit(args.log_file):
            return
        if evaluate_legacy_decision(args.log_file):
            return
        print("Tidak ada baris log DECISION_AUDIT maupun DECISION legacy yang valid.")
    except FileNotFoundError:
        print("File {} tidak ditemukan!".format(args.log_file))


if __name__ == "__main__":
    main()
