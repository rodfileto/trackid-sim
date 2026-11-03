"""Attributes for one synthetic person, and the prompt that renders them.

Split out of gen_pool.py so the prompts could later be built from the
simulated population. That plan was dropped (ROADMAP C1, 2026-09-22): the pool
is fixed, and the world binds its humans to these faces by sex and age instead.

What this does and does not fix
-------------------------------
It replaces one phenotype word with a set of physical descriptors, which is
better prompting: "deep brown skin, tightly coiled black hair" steers a
diffusion model harder than "Black Brazilian", because it describes pixels
rather than a social category. It is also the more honest formulation, since
"parda" spans a tonal range no single appearance represents.

It does **not** make the pool demographically representative. The first pool
already sampled phenotype words at these same shares and still rendered lighter
than asked: the skew lives in FLUX's weights, not in the prompt's structure.
So `realized_shares` exists to report what was asked for, and nothing here may
be read as ground truth about the rendered images -- PLAN.md scopes demographic
bias out of every claim built on this corpus, and that stands unchanged.
"""
import json
import random
from dataclasses import dataclass, asdict
from pathlib import Path

# Shares come from IBGE, fetched by fetch_shares.py and committed, so a run is
# reproducible offline and the one demographic input to the corpus is sourced
# rather than recalled. See that file for tables and attribution.
SHARES_FILE = Path(__file__).with_name("censo2022_shares.json")

# IBGE's category names -> the keys used here.
COLOUR_KEY = {"Parda": "parda", "Branca": "branca", "Preta": "preta",
              "Amarela": "amarela", "Indígena": "indigena"}


def load_shares(path=SHARES_FILE):
    """The committed Censo 2022 shares. Fails loudly rather than falling back.

    A silent default would be the worst outcome here: the pool would look
    sourced and not be.
    """
    if not path.exists():
        raise SystemExit(f"{path.name} missing -- run: uv run python fetch_shares.py")
    d = json.loads(path.read_text())
    return {
        "colour": {COLOUR_KEY[k]: v for k, v in d["colour"].items()},
        "sex": d["sex"],
        "age": {int(a): w for a, w in d["age"].items()},
        "provenance": {k: d[k] for k in ("source", "note", "fetched", "locality",
                                         "age_range", "tables")},
    }


# How each is named in the prompt. The ethnonym still carries facial structure
# that tone words do not, so it stays -- but tone leads, because tone is the
# descriptor the generator actually responds to.
ETHNICITY_PHRASE = {
    "parda":    "Brazilian person of mixed ancestry",
    "branca":   "White Brazilian person",
    "preta":    "Black Brazilian person",
    "amarela":  "East Asian Brazilian person",
    "indigena": "Indigenous Brazilian person",
}

# Tone conditioned on ethnicity, as a weighted list. The ranges overlap on
# purpose: that overlap is the point of decoupling tone from the label.
SKIN_TONES = {
    "parda":    [("light brown skin", 3), ("medium brown skin", 4),
                 ("olive brown skin", 2), ("deep tan skin", 2)],
    "branca":   [("very fair skin", 2), ("fair skin", 4), ("light olive skin", 2)],
    "preta":    [("dark brown skin", 4), ("deep brown skin", 4), ("very dark skin", 2)],
    "amarela":  [("light skin", 3), ("light golden skin", 2)],
    "indigena": [("copper-brown skin", 3), ("medium brown skin", 2)],
}

HAIR_TEXTURE = {
    "parda":    [("wavy", 4), ("curly", 4), ("straight", 2)],
    "branca":   [("straight", 5), ("wavy", 4), ("curly", 1)],
    "preta":    [("tightly coiled", 5), ("curly", 4), ("short afro-textured", 3)],
    "amarela":  [("straight", 9), ("wavy", 1)],
    "indigena": [("straight", 8), ("wavy", 2)],
}
HAIR_LENGTH = {
    "female": [("short", 2), ("shoulder-length", 4), ("long", 4)],
    "male":   [("close-cropped", 3), ("short", 5), ("medium-length", 2)],
}
# Greying is age-conditioned; below 40 it is rare enough to ignore here.
HAIR_COLOUR = [("black", 5), ("dark brown", 4), ("brown", 2), ("light brown", 1)]

FACIAL_HAIR = [("clean-shaven", 5), ("light stubble", 3),
               ("a short beard", 3), ("a full beard", 2), ("a moustache", 1)]

BUILD = [("slim", 3), ("average build", 5), ("stocky", 2), ("heavy-set", 2)]

# Brazilian identity photographs permit a closed-mouth smile, so the corpus
# should not be uniformly neutral -- expression is a real source of
# within-person variation for a recognizer.
SMILE_SHARE = 0.30


@dataclass(frozen=True)
class Person:
    id: str
    sex: str
    ethnicity: str
    age: int
    skin_tone: str
    hair: str
    facial_hair: str
    build: str
    expression: str
    seed: int

    def as_dict(self):
        return asdict(self)


def _pick(rng, weighted):
    values, weights = zip(*weighted)
    return rng.choices(values, weights=weights, k=1)[0]


def _allocate(count, weights):
    """Split `count` across keys by weight, largest-remainder.

    Deterministic allocation rather than per-draw sampling, so the realized
    shares are exact at any count instead of only in expectation -- 200 draws
    would otherwise leave the smallest groups visibly off.
    """
    total = sum(weights.values())
    exact = {k: count * w / total for k, w in weights.items()}
    alloc = {k: int(v) for k, v in exact.items()}
    left = count - sum(alloc.values())
    for k in sorted(exact, key=lambda k: (-(exact[k] - alloc[k]), k))[:left]:
        alloc[k] += 1
    return alloc


def sample_population(count, seed, smile_share=SMILE_SHARE, shares=None):
    """`count` people, a pure function of (count, seed, smile_share, shares).

    This is what the committed pool was drawn from. Colour, sex and single-year age all come from the
    committed IBGE shares; everything else (tone within a colour, hair, build,
    expression) is ours, because IBGE does not publish it.
    """
    shares = shares or load_shares()
    rng = random.Random(seed)
    # Colour and sex are allocated, not drawn: at 200 the smallest category is
    # about one person, and sampling would as easily give zero or three.
    ethnicities = [e for e, n in _allocate(count, shares["colour"]).items() for _ in range(n)]
    rng.shuffle(ethnicities)
    sexes = [s for s, n in _allocate(count, shares["sex"]).items() for _ in range(n)]
    rng.shuffle(sexes)
    ages, age_w = zip(*sorted(shares["age"].items()))

    people = []
    for i, eth in enumerate(ethnicities):
        sex = sexes[i]
        age = rng.choices(ages, weights=age_w, k=1)[0]
        texture = _pick(rng, HAIR_TEXTURE[eth])
        length = _pick(rng, HAIR_LENGTH[sex])
        colour = _pick(rng, HAIR_COLOUR)
        # Grey arrives with age rather than being drawn flat.
        if age >= 60 and rng.random() < 0.55:
            colour = "grey"
        elif age >= 45 and rng.random() < 0.25:
            colour = f"greying {colour}"
        facial = "clean-shaven"
        if sex == "male" and age >= 20:
            facial = _pick(rng, FACIAL_HAIR)
        people.append(Person(
            id=f"face_{i:03d}", sex=sex, ethnicity=eth, age=age,
            skin_tone=_pick(rng, SKIN_TONES[eth]),
            hair=f"{length} {texture} {colour} hair",
            facial_hair=facial,
            build=_pick(rng, BUILD),
            expression=("a slight closed-mouth smile" if rng.random() < smile_share
                        else "a neutral expression"),
            seed=rng.randrange(2**31),
        ))
    return people


def prompt_for(p):
    """One person -> one prompt. Tone first, then structure, then the rest."""
    noun = "woman" if p.sex == "female" else "man"
    parts = [
        f"Passport-style frontal photograph of a {p.age} year old {noun}",
        p.skin_tone,
        ETHNICITY_PHRASE[p.ethnicity],
        p.hair,
    ]
    if p.sex == "male" and p.facial_hair != "clean-shaven":
        parts.append(p.facial_hair)
    parts += [
        p.build,
        p.expression,
        "looking directly at the camera",
        "plain light grey background, even studio lighting, sharp focus, "
        "photorealistic, high detail",
    ]
    return ", ".join(parts)


def realized_shares(people):
    """What was *asked for*, for the run summary.

    Never what was rendered: the generator's own skew is not measured here and
    is not corrected by anything in this module.
    """
    out = {}
    for field in ("sex", "ethnicity", "expression"):
        counts = {}
        for p in people:
            counts[getattr(p, field)] = counts.get(getattr(p, field), 0) + 1
        out[field] = {k: (v, round(100 * v / len(people), 1))
                      for k, v in sorted(counts.items(), key=lambda kv: -kv[1])}
    ages = sorted(p.age for p in people)
    out["age"] = {"min": ages[0], "median": ages[len(ages) // 2], "max": ages[-1]}
    return out


if __name__ == "__main__":
    import json, sys
    n = int(sys.argv[1]) if len(sys.argv) > 1 else 200
    people = sample_population(n, 20260920)
    print(json.dumps(realized_shares(people), indent=1))
    print()
    for p in people[:6]:
        print(f"{p.id} {p.sex:6s} {p.ethnicity:8s} {p.age}\n  {prompt_for(p)}\n")
