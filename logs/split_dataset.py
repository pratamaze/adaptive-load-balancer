"""
split_dataset.py
----------------
Membagi file log telemetri CSV menjadi Train (70%) dan Test (30%)
menggunakan Chronological Split (urutan waktu dipertahankan).

Pembersihan data: baris dengan total_requests == 0 dihapus sebelum split.

Penggunaan:
    # Satu file
    python split_dataset.py base_normal.csv

    # Beberapa file sekaligus
    python split_dataset.py base_normal.csv base_spike.csv base_ramp.csv

    # Semua CSV di folder saat ini
    python split_dataset.py --all

Opsi tambahan:
    --ratio 0.7          Rasio train (default: 0.7)
    --invalid-col total_requests   Nama kolom penanda data invalid (default: total_requests)
    --output-dir ./out   Folder output (default: folder yang sama dengan file input)
"""

import argparse
import sys
from pathlib import Path

import pandas as pd


# ── Konstanta default ─────────────────────────────────────────────────────────
DEFAULT_TRAIN_RATIO = 0.70
DEFAULT_INVALID_COL = "total_requests"


# ── Fungsi utama pemrosesan satu file ─────────────────────────────────────────
def process_file(
    input_path: Path,
    train_ratio: float,
    invalid_col: str,
    output_dir: Path,
) -> dict:
    """
    Memproses satu file CSV: bersihkan → split → simpan.
    Mengembalikan dict ringkasan hasil untuk ditampilkan di terminal.
    """
    # 1. Baca CSV
    try:
        df = pd.read_csv(input_path)
    except Exception as e:
        return {"file": input_path.name, "status": f"GAGAL BACA: {e}"}

    total_raw = len(df)

    # 2. Validasi kolom penanda data invalid
    if invalid_col not in df.columns:
        return {
            "file": input_path.name,
            "status": (
                f"GAGAL: kolom '{invalid_col}' tidak ditemukan. "
                f"Kolom tersedia: {list(df.columns)}"
            ),
        }

    # 3. Bersihkan baris tidak valid (total_requests == 0)
    df_clean = df[df[invalid_col] != 0].reset_index(drop=True)
    n_removed = total_raw - len(df_clean)

    if len(df_clean) == 0:
        return {
            "file": input_path.name,
            "status": "GAGAL: tidak ada data valid tersisa setelah pembersihan.",
        }

    # 4. Chronological Split — urutan baris TIDAK diacak
    split_idx = int(len(df_clean) * train_ratio)
    df_train = df_clean.iloc[:split_idx]
    df_test = df_clean.iloc[split_idx:]

    # 5. Tentukan nama file output
    stem = input_path.stem          # misal: "base_normal"
    suffix = input_path.suffix      # ".csv"

    output_dir.mkdir(parents=True, exist_ok=True)
    train_path = output_dir / f"train_{stem}{suffix}"
    test_path = output_dir / f"test_{stem}{suffix}"

    # 6. Simpan
    df_train.to_csv(train_path, index=False)
    df_test.to_csv(test_path, index=False)

    return {
        "file": input_path.name,
        "status": "OK",
        "total_baris_mentah": total_raw,
        "baris_dihapus (total_requests=0)": n_removed,
        "baris_valid": len(df_clean),
        "train": len(df_train),
        "test": len(df_test),
        "rasio_aktual": f"{len(df_train)/len(df_clean):.1%} / {len(df_test)/len(df_clean):.1%}",
        "output_train": str(train_path),
        "output_test": str(test_path),
    }


# ── CLI ───────────────────────────────────────────────────────────────────────
def parse_args():
    parser = argparse.ArgumentParser(
        description="Chronological Train-Test Split untuk log telemetri CSV.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument(
        "files",
        nargs="*",
        help="File CSV yang akan diproses (bisa lebih dari satu).",
    )
    parser.add_argument(
        "--all",
        action="store_true",
        help="Proses semua file *.csv di direktori saat ini.",
    )
    parser.add_argument(
        "--ratio",
        type=float,
        default=DEFAULT_TRAIN_RATIO,
        metavar="FLOAT",
        help=f"Rasio data train (default: {DEFAULT_TRAIN_RATIO}).",
    )
    parser.add_argument(
        "--invalid-col",
        type=str,
        default=DEFAULT_INVALID_COL,
        metavar="NAMA_KOLOM",
        help=f"Kolom yang nilainya 0 dianggap invalid (default: '{DEFAULT_INVALID_COL}').",
    )
    parser.add_argument(
        "--output-dir",
        type=str,
        default=None,
        metavar="FOLDER",
        help="Folder tujuan output. Default: sama dengan folder file input.",
    )
    return parser.parse_args()


def collect_input_files(args) -> list[Path]:
    """Kumpulkan daftar file dari argumen CLI."""
    paths = []

    if args.all:
        paths = sorted(Path(".").glob("*.csv"))
        if not paths:
            print("⚠️  Tidak ada file .csv ditemukan di direktori saat ini.")
    else:
        for f in args.files:
            p = Path(f)
            if not p.exists():
                print(f"⚠️  File tidak ditemukan, dilewati: {f}")
            elif p.suffix.lower() != ".csv":
                print(f"⚠️  Bukan file CSV, dilewati: {f}")
            else:
                paths.append(p)

    return paths


def print_summary(result: dict):
    """Cetak ringkasan hasil satu file ke terminal."""
    file_name = result.get("file", "?")
    status = result.get("status", "?")

    if status != "OK":
        print(f"\n❌ {file_name}")
        print(f"   {status}")
        return

    print(f"\n✅ {file_name}")
    print(f"   Baris mentah        : {result['total_baris_mentah']}")
    print(f"   Dihapus (=0)        : {result['baris_dihapus (total_requests=0)']}")
    print(f"   Baris valid         : {result['baris_valid']}")
    print(f"   Train / Test        : {result['train']} / {result['test']} baris  ({result['rasio_aktual']})")
    print(f"   → {result['output_train']}")
    print(f"   → {result['output_test']}")


# ── Entry point ───────────────────────────────────────────────────────────────
def main():
    args = parse_args()

    # Validasi rasio
    if not (0.0 < args.ratio < 1.0):
        print("❌ --ratio harus di antara 0.0 dan 1.0 (eksklusif).")
        sys.exit(1)

    input_files = collect_input_files(args)

    if not input_files:
        print("❌ Tidak ada file yang diproses. Keluar.")
        sys.exit(1)

    print(f"\n{'='*55}")
    print(f"  Chronological Train-Test Split")
    print(f"  Rasio      : {args.ratio:.0%} train / {1-args.ratio:.0%} test")
    print(f"  Kolom filter: {args.invalid_col} == 0  →  dihapus")
    print(f"{'='*55}")

    for file_path in input_files:
        # Tentukan output_dir: gunakan --output-dir jika ada, jika tidak pakai folder file itu sendiri
        out_dir = Path(args.output_dir) if args.output_dir else file_path.parent
        result = process_file(
            input_path=file_path,
            train_ratio=args.ratio,
            invalid_col=args.invalid_col,
            output_dir=out_dir,
        )
        print_summary(result)

    print(f"\n{'='*55}")
    print("  Selesai.")
    print(f"{'='*55}\n")


if __name__ == "__main__":
    main()