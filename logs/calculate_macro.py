import csv
import re
import sys


NUMBER_RE = re.compile(r"[-+]?\d*\.?\d+")


def parse_number(raw_value):
    if raw_value is None:
        return None
    text = str(raw_value).strip().replace(",", ".")
    match = NUMBER_RE.search(text)
    if not match:
        return None
    try:
        return float(match.group(0))
    except Exception:
        return None


def safe_div(numerator, denominator):
    if denominator == 0:
        return 0.0
    return numerator / denominator


def mean(values):
    if not values:
        return 0.0
    return sum(values) / float(len(values))


def find_column(lower_to_original, aliases):
    for alias in aliases:
        key = alias.strip().lower()
        if key in lower_to_original:
            return lower_to_original[key]
    return None


def extract_cpu_series(rows, fieldnames):
    lower_to_original = {name.strip().lower(): name for name in fieldnames}

    node1_aliases = [
        "node1_cpu_normalized_usage",
        "node1_cpu_usage_normalized",
        "node1_cpu_normalized",
        "node_1_cpu_normalized_usage",
    ]
    node2_aliases = [
        "node2_cpu_normalized_usage",
        "node2_cpu_usage_normalized",
        "node2_cpu_normalized",
        "node_2_cpu_normalized_usage",
    ]

    node1_col = find_column(lower_to_original, node1_aliases)
    node2_col = find_column(lower_to_original, node2_aliases)

    cpu1_values = []
    cpu2_values = []

    # Mode wide: 1 row memuat CPU node1 + node2
    if node1_col and node2_col:
        for row in rows:
            v1 = parse_number(row.get(node1_col))
            v2 = parse_number(row.get(node2_col))
            if v1 is not None:
                cpu1_values.append(v1)
            if v2 is not None:
                cpu2_values.append(v2)
        return cpu1_values, cpu2_values

    # Mode long: ada node_name + cpu_usage_normalized
    node_col = find_column(lower_to_original, ["node_name"])
    cpu_col = find_column(lower_to_original, ["cpu_usage_normalized"])
    if not node_col or not cpu_col:
        return [], []

    for row in rows:
        node_name = str(row.get(node_col, "")).strip().lower()
        cpu_value = parse_number(row.get(cpu_col))
        if cpu_value is None:
            continue

        if re.search(r"node1|node_1|api-1|api1", node_name):
            cpu1_values.append(cpu_value)
        elif re.search(r"node2|node_2|api-2|api2", node_name):
            cpu2_values.append(cpu_value)

    return cpu1_values, cpu2_values


def evaluate_csv(file_path):
    try:
        with open(file_path, "r") as f:
            reader = csv.DictReader(f)
            fieldnames = reader.fieldnames or []
            rows = list(reader)
    except Exception as exc:
        print("[ERROR] Gagal membaca file {}: {}".format(file_path, exc))
        return

    if not rows:
        print("[ERROR] Dataset {} kosong!".format(file_path))
        return

    cpu1_values, cpu2_values = extract_cpu_series(rows, fieldnames)
    if not cpu1_values or not cpu2_values:
        print("[ERROR] Kolom cpu_usage_normalized Node 1/Node 2 tidak ditemukan atau kosong.")
        return

    avg_cpu_1 = mean(cpu1_values)
    avg_cpu_2 = mean(cpu2_values)
    avg_cpu_system = (avg_cpu_1 + avg_cpu_2) / 2.0

    di_macro = safe_div(abs(avg_cpu_1 - avg_cpu_2), avg_cpu_system)
    bcu_macro = safe_div(avg_cpu_system, max(avg_cpu_1, avg_cpu_2))

    print("AvgCPU_1: {:.6f}".format(avg_cpu_1))
    print("AvgCPU_2: {:.6f}".format(avg_cpu_2))
    print("AvgCPU_system: {:.6f}".format(avg_cpu_system))
    print("DI_macro: {:.6f}".format(di_macro))
    print("BCU_macro: {:.6f}".format(bcu_macro))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Cara penggunaan: python3 calculate_macro.py <nama_file.csv>")
        sys.exit(1)
    evaluate_csv(sys.argv[1])
