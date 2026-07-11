import numpy as np
import matplotlib.pyplot as plt
import seaborn as sns
import os

# ==========================================
# 1. PENGATURAN TEMA
# ==========================================
plt.style.use("seaborn-v0_8-whitegrid")
sns.set_theme(
    context="paper",
    style="whitegrid",
    rc={
        "axes.labelsize": 11,
        "xtick.labelsize": 10,
        "ytick.labelsize": 10,
        "legend.fontsize": 10,
        "lines.linewidth": 2.0,
        "figure.dpi": 300
    },
)

# Warna asli (Biru, Oranye, Hijau)
COLORS = ["#1f77b4", "#ff7f0e", "#2ca02c"]

OUTPUT_DIR = "tests/colab/output_fuzzy_final"
os.makedirs(OUTPUT_DIR, exist_ok=True)

# ==========================================
# 2. DATA HIMPUNAN FUZZY (DIPERBARUI)
# ==========================================
FUZZY_DATA = {
    "Normal": {
        "Utilisasi CPU (%)": {
            "Rendah": [0.00, 1.88, 11.20],
            "Sedang": [1.88, 11.88, 39.75],
            "Tinggi": [19.47, 45.94, 100.00]
        },
        "Panjang Antrean": {
            "Pendek": [0.00, 476.10, 802.36],
            "Sedang": [739.11, 882.69, 918.06],
            "Panjang": [882.69, 982.69, 1000.00]
        },
        "Waktu Respons (ms)": {
            "Cepat": [0.00, 1.31, 69.72],
            "Normal": [37.09, 101.31, 101.81],
            "Lambat": [101.55, 448.09, 1000.00]
        }
    },

    "Spike": {
        "Utilisasi CPU (%)": {
            "Rendah": [0.00, 19.20, 25.45],
            "Sedang": [19.20, 29.20, 29.25],
            "Tinggi": [29.20, 96.14, 100.00]
        },
        "Panjang Antrean": {
            "Pendek": [0.00, 0.00, 100.00],
            "Sedang": [61.05, 100.00, 100.50],
            "Panjang": [100.00, 200.00, 1000.00]
        },
        "Waktu Respons (ms)": {
            "Cepat": [0.27, 100.21, 173.09],
            "Normal": [138.80, 290.08, 1000.00],
            "Lambat": [138.80, 290.08, 1000.00]
        }
    },

    "Ramp": {
        "Utilisasi CPU (%)": {
            "Rendah": [0.00, 0.22, 6.20],
            "Sedang": [0.54, 10.22, 30.20],
            "Tinggi": [29.79, 45.08, 100.00]
        },
        "Panjang Antrean": {
            "Pendek": [0.00, 0.11, 50.18],
            "Sedang": [181.74, 182.24, 812.12],
            "Panjang": [182.24, 937.71, 1000.00]
        },
        "Waktu Respons (ms)": {
            "Cepat": [0.00, 82.24, 181.99],
            "Normal": [136.66, 137.16, 295.07],
            "Lambat": [137.44, 808.85, 1000.00]
        }
    }
}

# ==========================================
# 3. FUNGSI KURVA (LOGIKA BAHU TRAPESIUM)
# ==========================================
def get_membership_curve(mf_type, points, max_val):
    a, b, c = points
    a, b, c = max(0, a), min(max_val, b), min(max_val, c)
    
    if mf_type == "left":
        x = [0.0, b, c, max_val]
        y = [1.0, 1.0, 0.0, 0.0]
    elif mf_type == "right":
        x = [0.0, a, b, max_val]
        y = [0.0, 0.0, 1.0, 1.0]
    else: 
        x = [0.0, a, b, c, max_val]
        y = [0.0, 0.0, 1.0, 0.0, 0.0]
        
    return x, y

def generate_visualizations():
    for scenario_name, variables in FUZZY_DATA.items():
        # Canvas dibuat sedikit lebih tinggi (3.5) agar ada ruang untuk legend di atas
        fig, axes = plt.subplots(1, 3, figsize=(11, 3.5), sharey=True)
        
        for ax, (var_name, sets) in zip(axes, variables.items()):
            max_val = 100.0 if "CPU" in var_name else 1000.0
            
            set_names = list(sets.keys())
            for i, set_name in enumerate(set_names):
                points = sets[set_name]
                color = COLORS[i]
                
                mf_type = "left" if i == 0 else ("right" if i == len(set_names) - 1 else "middle")
                x, y = get_membership_curve(mf_type, points, max_val)
                
                # Plot garis tepi
                ax.plot(x, y, label=set_name, color=color, alpha=0.9)
                # Fill area dalam
                ax.fill_between(x, y, alpha=0.15, color=color)
            
            # --- PENGATURAN ESTETIKA ---
            ax.set_xlabel(var_name, fontweight="normal")
            ax.set_xlim(0, max_val)
            ax.set_ylim(0, 1.05)
            
            # Mempertahankan grid agar mudah dibaca nilainya
            ax.grid(True, linestyle='--', alpha=0.6)
            
            # Menghilangkan border atas dan kanan agar lebih bersih
            sns.despine(ax=ax)
            
            # LEGENDA DIPINDAH KE ATAS LUAR GRAFIK (Mendatar)
            ax.legend(loc="lower center", bbox_to_anchor=(0.5, 1.02), ncol=3, 
                      frameon=False, columnspacing=1.2, handletextpad=0.4)
        
        # Label Y hanya di panel paling kiri
        axes[0].set_ylabel("Derajat Keanggotaan (\u03BC)", fontweight="normal")
        
        # Pengaturan jarak antar panel
        plt.tight_layout()
        
        # Simpan file
        filename = os.path.join(OUTPUT_DIR, f"mf_{scenario_name.lower()}_top_legend.png")
        plt.savefig(filename, dpi=300, bbox_inches="tight")
        plt.close(fig)
        print(f"Tersimpan: {filename}")

if __name__ == "__main__":
    generate_visualizations()