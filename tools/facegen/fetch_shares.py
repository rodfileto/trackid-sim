"""Fetch the demographic shares the face prompts are sampled from, from IBGE.

Same principle as `fetch-lexicon` for the name lexicon: the numbers that shape
the corpus come from the source, with provenance recorded, rather than from
anyone's recollection. Writes `censo2022_shares.json`, which demographics.py
reads; the file is committed so a run is reproducible without the network.

Sources, both Censo 2022, Brazil (N1):

  colour   agregado 9605 "Populacao residente, por cor ou raca, nos Censos
           Demograficos", variable 1000093 (percent of total), class 86.
  sex/age  agregado 9514 "Populacao residente, por sexo, idade e forma de
           declaracao da idade", variable 93 (persons), class 2 x 287, with
           class 286 fixed to Total so the two declaration forms are not
           double counted. Single-year ages, so no band interpolation.

    uv run python fetch_shares.py
"""
import gzip
import json
import re
import urllib.request
from datetime import date, timezone, datetime

API = "https://servicodados.ibge.gov.br/api/v3/agregados"
OUT = "censo2022_shares.json"
AGE_MIN, AGE_MAX = 18, 75

# IBGE permits reuse citing IBGE as the source; a transformation must name who
# made it. Both travel with the file (ROADMAP block E).
SOURCE = "IBGE, Censo Demografico 2022, via servicodados.ibge.gov.br/api/v3/agregados"
NOTE = ("Shares extracted and renormalised over the adult range by trackid-sim "
        "(tools/facegen/fetch_shares.py). IBGE is the source of the underlying "
        "data and is not responsible for this transformation.")


def get(url):
    # Accept-Encoding: identity -- the API gzips by default and urllib, unlike
    # curl, does not decompress transparently.
    req = urllib.request.Request(url, headers={
        "Accept": "application/json",
        "User-Agent": "trackid-sim/facegen (+https://github.com/rodfileto/trackid)"})
    with urllib.request.urlopen(req, timeout=120) as r:
        body = r.read()
    # The API gzips regardless of Accept-Encoding, and urllib -- unlike curl --
    # does not decompress transparently. Detect by magic number rather than by
    # the header, which is not always set.
    if body[:2] == b"\x1f\x8b":
        body = gzip.decompress(body)
    return json.loads(body)


def categories(agregado, classificacao):
    meta = get(f"{API}/{agregado}/metadados")
    cls = next(c for c in meta["classificacoes"] if c["id"] == classificacao)
    return cls["categorias"], meta["nome"]


def series(agregado, variavel, classificacao_spec):
    url = (f"{API}/{agregado}/periodos/2022/variaveis/{variavel}"
           f"?localidades=N1&classificacao={classificacao_spec}")
    # Returns [(classification-id -> category-name, value)], not a positional
    # tuple: a query may pin extra classifications (9514 fixes 286 to Total)
    # and their order is the server's, not ours.
    out = []
    for var in get(url):
        for r in var["resultados"]:
            cls = {c["id"]: list(c["categoria"].values())[0] for c in r["classificacoes"]}
            for s in r["series"]:
                out.append((cls, s["serie"]["2022"]))
    return out


def main():
    # --- colour -----------------------------------------------------------
    cats, name_9605 = categories(9605, 86)
    ids = [c["id"] for c in cats if c["nivel"] == 1]
    raw = series(9605, 1000093, f"86[{','.join(str(i) for i in ids)}]")
    colour = {cls["86"]: float(v) for cls, v in raw}
    # The five categories are exhaustive but round to 99.99; renormalise so the
    # weights sum to exactly 1 and no share is silently lost.
    tot = sum(colour.values())
    colour = {k: v / tot for k, v in sorted(colour.items(), key=lambda kv: -kv[1])}

    # --- sex x single-year age over the adult range -----------------------
    cats, name_9514 = categories(9514, 287)
    years = {}
    for c in cats:
        m = re.fullmatch(r"(\d+) anos?", c["nome"])
        if m and AGE_MIN <= int(m.group(1)) <= AGE_MAX:
            years[int(m.group(1))] = c["id"]
    spec = (f"2[4,5]|287[{','.join(str(years[a]) for a in sorted(years))}]|286[113635]")
    raw = series(9514, 93, spec)

    by_sex = {"male": 0, "female": 0}
    by_age = {}
    label = {"Homens": "male", "Mulheres": "female"}
    for cls, v in raw:
        n = int(v)
        age = int(re.match(r"(\d+)", cls["287"]).group(1))
        by_sex[label[cls["2"]]] += n
        by_age[age] = by_age.get(age, 0) + n
    tot_sex = sum(by_sex.values())
    tot_age = sum(by_age.values())

    doc = {
        "source": SOURCE,
        "note": NOTE,
        "fetched": datetime.now(timezone.utc).date().isoformat(),
        "locality": "Brasil (N1)",
        "age_range": [AGE_MIN, AGE_MAX],
        "tables": {"colour": f"9605 - {name_9605}", "sex_age": f"9514 - {name_9514}"},
        "population_adults": tot_age,
        "colour": {k: round(v, 6) for k, v in colour.items()},
        "sex": {k: round(v / tot_sex, 6) for k, v in by_sex.items()},
        # Single-year weights, so no interpolation inside a band.
        "age": {str(a): round(by_age[a] / tot_age, 8) for a in sorted(by_age)},
    }
    with open(OUT, "w") as f:
        json.dump(doc, f, indent=1)
        f.write("\n")

    print(f"{OUT}: adults {AGE_MIN}-{AGE_MAX} = {tot_age:,}")
    for k, v in doc["colour"].items():
        print(f"  {k:10s} {100*v:6.2f}%")
    print(f"  male {100*doc['sex']['male']:.2f}%  female {100*doc['sex']['female']:.2f}%")


if __name__ == "__main__":
    main()
