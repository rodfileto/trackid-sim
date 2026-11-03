"""Sketch: one synthetic fingerprint, every stage on one review sheet.

SFinGe-style (Cappelli & Maltoni): singular points -> orientation field ->
ridge frequency -> ridges grown from seeds by iterated space-variant Gabor
filtering -> master print. Then one rolled impression and one latent of that
master. Deterministic given --seed. A throwaway prototype for review; the
generator proper is meant to be ported to Go.

    uv run sketch_one.py --seed 7
"""

import argparse

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
from scipy import ndimage
from scipy.signal import fftconvolve
from skimage.morphology import skeletonize

H, W = 480, 400  # px at 500 dpi: ~24 x 20 mm
N_ORIENT = 16  # orientation bins for the space-variant filter
PERIODS = (7.5, 8.25, 9.0, 9.75, 10.5)  # ridge period bins, px (~0.45 mm at 500 dpi)


def fingertip_mask(rng):
    """Superellipse: round at the tip (p=2), squarer towards the joint (p=4)."""
    y, x = np.mgrid[0:H, 0:W].astype(float)
    cx, cy = W / 2, H * 0.52
    a, b = W * rng.uniform(0.40, 0.45), H * rng.uniform(0.44, 0.48)
    u, v = (x - cx) / a, (y - cy) / b
    p = np.where(v < 0, 2.0, 4.0)
    return np.abs(u) ** p + np.abs(v) ** p <= 1.0


def singular_points(rng, cls):
    """Core and delta positions for a pattern class, image coords (x, y).

    Placement varies widely on purpose. With the core pinned near the centre
    and the delta at a near-fixed offset, every loop shared one flow field,
    the flow forced minutiae into the same places, and same-class impostors
    matched: 1.02% at SourceAFIS threshold 40 against 0.03% across classes.
    The core-delta distance is also what sets the ridge count between them,
    which varies a lot between real loops."""
    cx = W / 2 + rng.normal(0, 22)
    cy = H * 0.42 + rng.normal(0, 22)
    if cls == "arch":
        return [], []
    if cls in ("left_loop", "right_loop"):
        side = 1 if cls == "left_loop" else -1
        dist, ang = rng.uniform(80, 190), np.deg2rad(rng.uniform(35, 70))
        return [(cx, cy)], [(cx + side * dist * np.cos(ang), cy + dist * np.sin(ang))]
    if cls == "whorl":
        dy, tilt = rng.uniform(8, 30), rng.normal(0, 8)
        d1, d2 = rng.uniform(90, 170, 2)
        a1, a2 = np.deg2rad(rng.uniform(35, 65, 2))
        return (
            [(cx - tilt, cy - dy), (cx + tilt, cy + dy)],
            [(cx - d1 * np.cos(a1), cy + d1 * np.sin(a1)), (cx + d2 * np.cos(a2), cy + d2 * np.sin(a2))],
        )
    raise ValueError(cls)


def orientation_field(cores, deltas, rng):
    """Zero-pole model (Sherlock & Monro) near the singular points, blended
    into a fingertip flow above them. Radians, ridge direction, mod pi.

    Zero-pole alone has a constant far field (total index 0), so ridges cross
    the tip flat; real ridges arch around it. Blending is done on doubled
    angles so theta and theta+pi agree."""
    y, x = np.mgrid[0:H, 0:W].astype(float)
    z = x + 1j * y
    def zero_pole(zz, core_maps):
        t = np.zeros(np.shape(zz))
        for c, (a_open, k) in zip(cores, core_maps):
            a = np.angle(zz - complex(*c))
            t += 0.5 * (a - k * np.sin(a - a_open))
        for d in deltas:
            t -= 0.5 * np.angle(zz - complex(*d))
        return t

    # Loop legs follow the one ray out of each core along which the ridges
    # run radially; with plain zero-pole they fan out along neighbouring rays
    # and the loop head pinches to a point. Remap each core's angle
    # (Vizcaya & Gerhardt style) as a - k sin(a - a_open): slope 1 - k at
    # the opening, so the angles around it change slowly and the legs run
    # nearly parallel; still monotonic and 2pi-periodic for k < 1.
    maps = [(0.0, 0.0)] * len(cores)
    for i, c in enumerate(cores):
        alphas = np.linspace(-np.pi, np.pi, 720, endpoint=False)
        ring = complex(*c) + 40 * np.exp(1j * alphas)
        off = np.abs(np.sin(zero_pole(ring, maps) - alphas))
        maps[i] = (alphas[np.argmin(off)], rng.uniform(0.55, 0.75))
    sp = zero_pole(z, maps)
    if not cores:  # plain arch: ridges bow upward across the finger
        hump = rng.uniform(0.45, 0.7)
        sp = -hump * np.sin(np.pi * (x - W / 2) / W) * np.exp(-((y - H * 0.5) / (H * 0.25)) ** 2)

    # fingertip flow: tangent to ellipses centred below the tip, wider than
    # the finger so the sides slope down rather than turn vertical
    ex, ey, ea, eb = W / 2, H * 0.60, W * 0.50, H * 0.50
    bg = np.arctan2((y - ey) / eb**2, (x - ex) / ea**2) + np.pi / 2

    # start the blend where the fingertip flow is still near horizontal: the
    # two fields must not be ~90 deg apart there, or the blend snaps into a seam
    top = min((c[1] for c in cores), default=H * 0.36)
    pts = cores + deltas
    dist = np.min([np.hypot(x - px, y - py) for px, py in pts], axis=0) if pts else np.full((H, W), 1e9)
    w = np.clip((top - 20 - y) / 110, 0, 1) * np.clip((dist - 30) / 80, 0, 1)
    w = w * w * (3 - 2 * w)  # smoothstep
    c2 = (1 - w) * np.cos(2 * sp) + w * np.cos(2 * bg)
    s2 = (1 - w) * np.sin(2 * sp) + w * np.sin(2 * bg)
    theta = 0.5 * np.arctan2(s2, c2)
    return np.mod(theta + rng.normal(0, 0.03), np.pi)


def frequency_map(rng):
    """Ridge period: base 8.3-9.7 px per finger, a broad +/-8% drift plus a finer +/-6%
    ripple. Where the period changes the ridge count must change, which is
    one of the places minutiae come from."""
    broad = ndimage.gaussian_filter(rng.normal(0, 1, (H, W)), 40)
    fine = ndimage.gaussian_filter(rng.normal(0, 1, (H, W)), 12)
    broad /= np.abs(broad).max() + 1e-9
    fine /= np.abs(fine).max() + 1e-9
    base = rng.uniform(8.3, 9.7)  # each finger its own ridge spacing
    return base * (1 + 0.08 * broad + 0.06 * fine)


def gabor(theta, period, size=25):
    """Even Gabor: cosine across the ridges, elongated along them."""
    r = size // 2
    y, x = np.mgrid[-r : r + 1, -r : r + 1].astype(float)
    along = x * np.cos(theta) + y * np.sin(theta)
    across = -x * np.sin(theta) + y * np.cos(theta)
    sx, sy = period * 0.55, period * 0.9
    g = np.exp(-0.5 * (across**2 / sx**2 + along**2 / sy**2)) * np.cos(2 * np.pi * across / period)
    return g - g.mean()


def grow_ridges(theta, period, mask, rng, n_seeds=60, max_iter=80, snapshots=(2, 5, 8)):
    """Iterate: filter each pixel with the Gabor for its own orientation and
    period, then clip. Ridges spread from the seeds; minutiae form where
    growing fronts meet out of phase."""
    o_bin = np.round(theta / np.pi * N_ORIENT).astype(int) % N_ORIENT
    p_bin = np.argmin([np.abs(period - p) for p in PERIODS], axis=0)
    bank = [[gabor(o * np.pi / N_ORIENT, p) for p in PERIODS] for o in range(N_ORIENT)]

    img = np.zeros((H, W))
    ys, xs = np.nonzero(mask)
    pick = rng.choice(len(ys), n_seeds, replace=False)
    img[ys[pick], xs[pick]] = rng.choice([-1.0, 1.0], n_seeds)
    seeds = img.copy()

    snaps = {}
    inner = ndimage.binary_erosion(mask, iterations=6)
    covered_at = None
    for it in range(1, max_iter + 1):
        out = np.zeros((H, W))
        for o in range(N_ORIENT):
            for k in range(len(PERIODS)):
                sel = (o_bin == o) & (p_bin == k)
                if sel.any():
                    out[sel] = fftconvolve(img, bank[o][k], mode="same")[sel]
        m = np.abs(out).max() + 1e-9
        img = np.clip(out / m * 4.0, -1, 1)
        if it in snapshots:
            snaps[it] = img.copy()
        # Stop once the ridges cover the finger. Iterating on until the
        # pattern stops moving anneals away the defects where growth fronts
        # met, and those defects are the minutiae.
        # Two passes after coverage clean up the ridge edges.
        if covered_at is None and (ndimage.gaussian_filter(img**2, 5)[inner] > 0.15).mean() > 0.995:
            covered_at = it
        if covered_at is not None and it >= covered_at + 2 and it >= max(snapshots):
            break
    return seeds, snaps, img, it


def add_minutiae_pairs(master, theta, period, mask, pts, rng, n_pairs):
    """Gabor growth tops out around 50-60 minutiae, bunched where the flow
    forces them; a rolled print this size has ~80-100 spread over the whole
    finger. Add them as dipoles: two opposite phase spirals, applied as a
    shift across the ridges of (phase/2pi) * period. A dipole's phase decays
    as 1/r, so unlike a lone minutia it leaves the far ridges untouched.
    Depending on its axis a dipole reads as a broken ridge, a short ridge, an
    enclosure or two separate minutiae."""
    y, x = np.mgrid[0:H, 0:W].astype(float)
    inner = ndimage.binary_erosion(mask, iterations=25)
    placed, out = [], master.copy()
    tries = 0
    while len(placed) < n_pairs and tries < 5000:
        tries += 1
        cy, cx = rng.integers(H), rng.integers(W)
        if not inner[cy, cx]:
            continue
        # keep clear of the singular points (curved flow) and of other pairs
        if any(np.hypot(cx - px, cy - py) < 45 for px, py in pts):
            continue
        if any(np.hypot(cx - px, cy - py) < 40 for px, py in placed):
            continue
        placed.append((cx, cy))
        t, T = theta[cy, cx], period[cy, cx]
        n = np.array([-np.sin(t), np.cos(t)])  # normal to the ridges, fixed locally
        half = rng.uniform(8, 30)
        a = rng.uniform(0, np.pi)  # dipole axis, any angle to the ridges
        d = half * np.array([np.cos(a), np.sin(a)])
        ang1 = np.arctan2(y - (cy + d[1]), x - (cx + d[0]))
        ang2 = np.arctan2(y - (cy - d[1]), x - (cx - d[0]))
        dpsi = np.angle(np.exp(1j * (ang1 - ang2)))  # wrap: the cut sits between the pair
        R = half * 4 + 30
        win = np.clip(1 - ((x - cx) ** 2 + (y - cy) ** 2) / R**2, 0, 1) ** 2
        shift = dpsi / (2 * np.pi) * T * win
        # resample only inside the window, so pairs do not blur the whole print
        box = (slice(max(0, int(cy - R)), int(cy + R) + 1), slice(max(0, int(cx - R)), int(cx + R) + 1))
        out[box] = ndimage.map_coordinates(
            out, [(y - shift * n[1])[box], (x - shift * n[0])[box]], order=3, mode="nearest"
        )
    # resampling still softens where windows overlap; restore ridge contrast
    return np.tanh(2.5 * out) / np.tanh(2.5), placed


def minutiae(master, mask):
    """Crossing number on the skeleton: CN=1 ending, CN=3 bifurcation.
    A stand-in for MINDTCT, good enough to count and show them."""
    inner = ndimage.binary_erosion(mask, iterations=18)
    sk = skeletonize(master > 0)
    p = np.pad(sk, 1).astype(int)
    nb = [p[0:-2, 1:-1], p[0:-2, 2:], p[1:-1, 2:], p[2:, 2:], p[2:, 1:-1], p[2:, 0:-2], p[1:-1, 0:-2], p[0:-2, 0:-2]]
    cn = sum(np.abs(nb[i] - nb[(i + 1) % 8]) for i in range(8)) // 2
    pts = []
    for kind, val in (("ending", 1), ("bifurcation", 3)):
        for y, x in zip(*np.nonzero(sk & (cn == val) & inner)):
            pts.append((x, y, kind))
    # drop clusters (spurs, short breaks): anything within 7 px of another
    keep = []
    for i, (x, y, k) in enumerate(pts):
        if all((x - x2) ** 2 + (y - y2) ** 2 > 49 for j, (x2, y2, _) in enumerate(pts) if j != i):
            keep.append((x, y, k))
    return sk, keep


def warp_coords(rng, amp, rot_deg, shift, smooth=60):
    """Elastic skin distortion: smooth random displacement + rigid motion.
    Returns sampling coordinates, so the print and its outline share one warp."""
    y, x = np.mgrid[0:H, 0:W].astype(float)
    dx = ndimage.gaussian_filter(rng.normal(0, 1, (H, W)), smooth)
    dy = ndimage.gaussian_filter(rng.normal(0, 1, (H, W)), smooth)
    dx *= amp / (np.abs(dx).max() + 1e-9)
    dy *= amp / (np.abs(dy).max() + 1e-9)
    a = np.deg2rad(rot_deg)
    xc, yc = x - W / 2, y - H / 2
    sx = np.cos(a) * xc - np.sin(a) * yc + W / 2 + dx - shift[0]
    sy = np.sin(a) * xc + np.cos(a) * yc + H / 2 + dy - shift[1]
    return [sy, sx]


def smooth_noise(rng, sigma):
    """Gaussian-filtered white noise, rescaled to unit standard deviation."""
    n = ndimage.gaussian_filter(rng.normal(0, 1, (H, W)), sigma)
    return (n - n.mean()) / (n.std() + 1e-9)


def creases(rng, n, width=(1.5, 3.5)):
    """White lines where skin folds: gently curved, mostly across the finger."""
    y, x = np.mgrid[0:H, 0:W].astype(float)
    out = np.zeros((H, W))
    for _ in range(n):
        cx, cy = rng.uniform(0.2, 0.8) * W, rng.uniform(0.35, 0.95) * H
        t = rng.normal(0, 0.45)  # radians from horizontal
        length, bend, w = rng.uniform(40, 160), rng.normal(0, 0.004), rng.uniform(*width)
        u = (x - cx) * np.cos(t) + (y - cy) * np.sin(t)
        v = -(x - cx) * np.sin(t) + (y - cy) * np.cos(t) - bend * u**2
        along = np.clip(1 - (2 * u / length) ** 2, 0, 1)  # tapers at both ends
        out = np.maximum(out, np.exp(-0.5 * (v / w) ** 2) * along)
    return out


def rolled_impression(master, mask, rng, rot=6, shift_sd=6, amp=5):
    """Ink on card, dark ridges on white. Pressure sets ridge width; dry
    patches break ridges; over-inked patches merge them; ridge edges are
    rough; pores show as light dots on ridges; creases cross the ridges."""
    coords = warp_coords(rng, amp=amp, rot_deg=rng.uniform(-rot, rot), shift=rng.normal(0, shift_sd, 2))
    m = ndimage.map_coordinates(master, coords, order=3, cval=-1)
    mk = ndimage.map_coordinates(mask.astype(float), coords, order=1, cval=0)

    level = rng.uniform(-0.2, 0.2) + 0.25 * smooth_noise(rng, 35)  # pressure, varies over the finger
    dry = np.clip(-smooth_noise(rng, 6) - 1.0, 0, None)  # rare dry breaks
    blot = np.clip(smooth_noise(rng, 15) - 2.0, 0, None)  # rare over-inked blots
    rough = 0.12 * smooth_noise(rng, 1.2)  # ridge-edge roughness
    ink = 1 / (1 + np.exp(-(m + level + rough - 0.5 * dry + 1.2 * blot) * 5))

    # pores: sparse light dots on ridge centres, ~0.4 mm apart along a ridge
    pore_seed = (rng.random((H, W)) < 0.008) & (m > 0.8)
    pores = ndimage.gaussian_filter(pore_seed.astype(float), 1.0) * 6
    ink *= 1 - np.clip(pores, 0, 0.7)

    ink *= 1 - creases(rng, int(rng.integers(2, 7)))
    contact = ndimage.gaussian_filter(mk, 6) * (0.85 + 0.15 * smooth_noise(rng, 3).clip(-1, 1))
    gray = 1 - 0.88 * ink * np.clip(contact, 0, 1)
    gray = ndimage.gaussian_filter(gray, 0.6) + 0.025 * rng.normal(0, 1, (H, W))
    return np.clip(gray, 0, 1)


def latent(master, mask, rng):
    """A partial, smudged, low-contrast mark, powdered on a light textured
    surface. Crude: latent realism is a stated limitation, not a claim."""
    coords = warp_coords(rng, amp=9, rot_deg=rng.uniform(-25, 25), shift=rng.normal(0, 10, 2), smooth=45)
    m = ndimage.map_coordinates(master, coords, order=3, cval=-1)
    mk = ndimage.map_coordinates(mask.astype(float), coords, order=1, cval=0) > 0.5
    y, x = np.mgrid[0:H, 0:W].astype(float)

    # deposit: an irregular blob (ellipse perturbed by noise) inside the finger
    ys, xs = np.nonzero(ndimage.binary_erosion(mk, iterations=90))
    i = rng.integers(len(ys))
    cy, cx = ys[i], xs[i]
    a, b, t = rng.uniform(95, 130), rng.uniform(70, 100), rng.uniform(0, np.pi)
    u = ((x - cx) * np.cos(t) + (y - cy) * np.sin(t)) / a
    v = (-(x - cx) * np.sin(t) + (y - cy) * np.cos(t)) / b
    region = (1 - np.hypot(u, v) + 0.35 * smooth_noise(rng, 18) > 0) & mk
    region = ndimage.binary_opening(region, iterations=3)
    lab, n = ndimage.label(region)
    if n > 1:  # keep the largest piece
        region = lab == 1 + np.argmax(ndimage.sum(region, lab, range(1, n + 1)))
    blob = ndimage.gaussian_filter(region.astype(float), 4)
    blob *= np.clip(0.75 + 0.35 * smooth_noise(rng, 10), 0, 1)  # uneven pressure: ridges drop out

    residue = np.clip(m, 0, 1) * blob  # sweat/oil left by ridges only
    smear = ndimage.uniform_filter1d(residue, size=int(rng.uniform(6, 14)), axis=int(rng.integers(2)))
    part = smooth_noise(rng, 25) > 0.6
    residue = np.where(part, smear, residue)

    # surface: fine grain dominates, faint larger mottling, a few scratches
    surface = 0.80 + 0.035 * smooth_noise(rng, 1.5) + 0.02 * smooth_noise(rng, 6) + 0.02 * smooth_noise(rng, 40)
    surface -= 0.10 * creases(rng, int(rng.integers(0, 4)), width=(0.6, 1.2))
    powder = 0.06 * (rng.random((H, W)) < 0.02) * rng.random((H, W))  # stray powder specks
    gray = surface - powder - 0.36 * ndimage.gaussian_filter(residue, 1.2) + 0.03 * rng.normal(0, 1, (H, W))
    return np.clip(gray, 0, 1), region


def orientation_lines(ax, theta, mask, step=20):
    ax.imshow(np.where(mask, 0.95, 0.75), cmap="gray", vmin=0, vmax=1)
    for y in range(step // 2, H, step):
        for x in range(step // 2, W, step):
            if mask[y, x]:
                t = theta[y, x]
                dx, dy = 8 * np.cos(t), 8 * np.sin(t)
                ax.plot([x - dx, x + dx], [y - dy, y + dy], "k-", lw=1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--cls", default=None, help="arch | left_loop | right_loop | whorl (default: sampled)")
    ap.add_argument("--out", default="samples/fp_sketch.png")
    args = ap.parse_args()

    rng = np.random.default_rng(args.seed)
    # rough population shares of the main classes; tented arch folded into arch
    classes, shares = ["left_loop", "right_loop", "whorl", "arch"], [0.33, 0.30, 0.30, 0.07]
    cls = args.cls or rng.choice(classes, p=shares)

    mask = fingertip_mask(rng)
    cores, deltas = singular_points(rng, cls)
    theta = orientation_field(cores, deltas, rng)
    period = frequency_map(rng)
    seeds, snaps, grown, iters = grow_ridges(theta, period, mask, rng)
    pts = cores + deltas
    master, pairs = add_minutiae_pairs(grown, theta, period, mask, pts, rng, n_pairs=int(rng.integers(18, 26)))
    master = np.where(mask, master, 0.0)
    skel, mins = minutiae(master, mask)
    rolled = rolled_impression(master, mask, rng)
    lat, lat_region = latent(master, mask, rng)

    n_end = sum(k == "ending" for *_, k in mins)
    n_bif = len(mins) - n_end
    print(f"class={cls} iterations={iters} minutiae={len(mins)} (endings={n_end}, bifurcations={n_bif})")

    fig, axs = plt.subplots(3, 4, figsize=(16, 15.5))
    for ax in axs.flat:
        ax.set_xticks([])
        ax.set_yticks([])

    ax = axs[0, 0]
    ax.imshow(mask, cmap="gray", vmin=-0.3, vmax=1.3)
    for x, y in cores:
        ax.plot(x, y, "o", ms=12, mfc="none", mec="tab:red", mew=2.5)
    for x, y in deltas:
        ax.plot(x, y, "^", ms=12, mfc="none", mec="tab:blue", mew=2.5)
    ax.set_title(f"1. fingertip + singular points\nclass={cls}  (o core, ^ delta)")

    orientation_lines(axs[0, 1], theta, mask)
    axs[0, 1].set_title("2. orientation field\nzero-pole + fingertip flow")

    im = axs[0, 2].imshow(np.where(mask, period, np.nan), cmap="viridis")
    fig.colorbar(im, ax=axs[0, 2], fraction=0.046, label="ridge period (px)")
    axs[0, 2].set_title("3. ridge frequency\n~9 px = 0.46 mm at 500 dpi")

    ax = axs[0, 3]
    ax.imshow(np.where(mask, 0.95, 0.75), cmap="gray", vmin=0, vmax=1)
    sy, sx = np.nonzero(seeds)
    ax.scatter(sx, sy, c=np.where(seeds[sy, sx] > 0, "k", "tab:orange"), s=40)
    ax.set_title(f"4. seeds ({len(sx)} impulses)")

    for ax, (it, s) in zip(axs[1, :3], sorted(snaps.items())):
        ax.imshow(np.where(mask, -s, 0.4), cmap="gray", vmin=-1, vmax=1)
        ax.set_title(f"5. Gabor growth, iteration {it}")
    axs[1, 3].imshow(np.where(mask, -master, 1), cmap="gray", vmin=-1, vmax=1)
    axs[1, 3].set_title(f"6. master print\n(covered at iteration {iters})")

    ax = axs[2, 0]
    ax.imshow(np.where(mask, ~skel, 1), cmap="gray")
    for x, y, k in mins:
        ax.plot(x, y, "o" if k == "ending" else "s", ms=7, mfc="none",
                mec="tab:red" if k == "ending" else "tab:blue", mew=1.6)
    ax.set_title(f"7. skeleton + minutiae: {len(mins)}\n(o ending {n_end}, [] bifurcation {n_bif})")

    axs[2, 1].imshow(rolled, cmap="gray", vmin=0, vmax=1)
    axs[2, 1].set_title("8. rolled impression (KNOWN)\ndistortion, pressure, dry patches, noise")

    axs[2, 2].imshow(lat, cmap="gray", vmin=0, vmax=1)
    axs[2, 2].set_title("9. latent (QUESTIONED)\npartial, smudged, textured surface")

    ax = axs[2, 3]
    ax.imshow(lat, cmap="gray", vmin=0, vmax=1)
    ax.contour(lat_region, levels=[0.5], colors="tab:red", linewidths=1.5)
    ax.set_title(f"10. latent deposit region\n{lat_region.sum() / mask.sum():.0%} of finger area")

    fig.suptitle(f"One synthetic fingerprint, seed={args.seed}", fontsize=16)
    fig.tight_layout()
    fig.savefig(args.out, dpi=80)
    print(f"wrote {args.out}")
    # native 500 dpi images, side by side, for looking at ridge detail
    stem = args.out.rsplit(".", 1)[0]
    strip = np.hstack([np.where(mask, (1 - master) / 2, 1), rolled, lat])
    plt.imsave(f"{stem}_500dpi.png", strip, cmap="gray", vmin=0, vmax=1)
    print(f"wrote {stem}_500dpi.png")


if __name__ == "__main__":
    main()
