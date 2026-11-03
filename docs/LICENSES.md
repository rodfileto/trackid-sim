# Licences of models and data

What we are allowed to do with every model and dataset that touches the corpus,
and why the rejected ones were rejected. Required by [ROADMAP.md](ROADMAP.md)
block C0 and re-read in full before any release (block E).

Each entry records **where the terms were read and when**. An entry marked
*not yet read* is a gate, not a detail: nothing ships on an unread licence.

## The two tracks

From [PLAN.md](PLAN.md): research use (papers, this repo) may rely on
non-commercial terms; the open-source trackid app, which real organizations may
deploy, may not. So nothing restricted shapes trackid — code, defaults,
thresholds, fixtures, bundled models — or the released corpus.

A licence that is fine for a paper can still be wrong for this repo. When in
doubt the stricter track wins, because the corpus is meant to be redistributed.

## Generators we use

### FLUX.1-schnell (base faces)

- **Repo**: `black-forest-labs/FLUX.1-schnell` on Hugging Face.
- **Licence**: Apache-2.0. Full text read 2026-09-19 from the model card.
- **Access**: the repo is *gated*. An anonymous download fails with
  `GatedRepoError` (401); it needs a Hugging Face account that has accepted the
  repo's access agreement, plus a token. Accepting that agreement is a condition
  of access and is **separate from** the Apache-2.0 grant on the weights.
- **What Apache-2.0 gives us**: §2 grants use, reproduction and distribution,
  including of derivative works. Outputs of the model are not themselves
  derivative works of the weights, so generated images are ours to release.
- **What it asks in return** (§4, if we ever redistribute the *weights* or
  modified files): ship the licence, mark changed files, keep attribution and
  `NOTICE` contents. Releasing only generated images avoids all of this — that
  is the intended route.
- **§7/§8**: no warranty, no liability. Nothing here is fitness for forensic use.

### Qwen-Image-Edit (variants of the same face)

- **Repo**: `Qwen/Qwen-Image-Edit` on Hugging Face. Access confirmed with our
  token 2026-09-19.
- **Licence**: Apache-2.0 per the model card *per [ROADMAP.md](ROADMAP.md)*.
  **Full text not yet read in place.** Gate before any variant image is
  released.
- **Components actually used**: the Qwen2.5-VL text encoder, the VAE and the
  transformer. The encoder is a vision-language model that reads the input
  image, so it is covered by the same repo and the same terms.

### QuantStack/Qwen-Image-Edit-GGUF (the quantization we run)

The 20B transformer does not fit here in fp16, so we run a third party's
pre-quantized build rather than Qwen's own weights. That is an extra link in the
chain and is recorded as such.

- **Repo**: `QuantStack/Qwen-Image-Edit-GGUF`, file `Qwen_Image_Edit-Q4_K_M.gguf`
  (12.17 GB). Checked 2026-09-20.
- **Licence tag**: `apache-2.0`, with `base_model: Qwen/Qwen-Image-Edit`
  declared — consistent with the upstream terms, which is what a requant must
  be. **Read the repo's own card in place before release**, alongside Qwen's.
- **Why it matters**: a quantization is a derivative of the original weights. If
  its terms ever diverged from Apache-2.0 it would contaminate the outputs, so
  this entry is checked whenever the file or repo changes.

## Models that score the corpus (trackid-vision)

These do not generate images; they produce the embeddings and detections that
replace the parametric scores. They ship inside trackid, so the **app track**
applies: non-commercial terms here would be disqualifying. Terms below read
2026-09-19 from the `trackid-vision` checkout (its README "Model licenses" table
and `vision/onnx.go`); the library itself is MIT.

- **AuraFace-v1** (recognizer, `glintr100.onnx`, from
  [`fal/AuraFace-v1`](https://huggingface.co/fal/AuraFace-v1)) — Apache-2.0 per
  the model card. fal states it was trained on commercially and publicly
  available data "to enable its usage in commercial setting"; the training data
  itself is not disclosed. **Clears both tracks.**
- **YuNet** (detector, `face_detection_yunet_2026may.onnx`, OpenCV Zoo, Shiqi
  Yu) — MIT for the model, BSD-3-Clause for its training code. Trained on WIDER
  Face, whose own licence is CC BY-NC-ND; whether that reaches trained weights
  is unsettled, and the author states no restriction. **Usable, with that
  caveat recorded.**
- **SCRFD** (detector, `scrfd_10g_bnkps.onnx`, InsightFace) — non-commercial
  research use only. **Fails the app track.** See the trap below.

### Trap (fixed upstream 2026-10-20): SCRFD was trackid-vision's default detector

Up to v0.3.0, `vision.Config.Detector` defaulted to `DetectorSCRFD`: code that
left `Detector` unset silently picked the **non-commercial** model. Since
2026-10-20 (the release after v0.3.0) the empty string resolves to YuNet and
SCRFD is opt-in; trackid-vision also moved from MIT to Apache-2.0 the same day,
like trackid and this repo.

The simulator still sets `Detector: vision.DetectorYuNet` **explicitly**
wherever it embeds (`tools/facescore` builds against the local checkout;
`../trackid` pins v0.3.0 until it is bumped, and passes `VISION_DETECTOR`,
default `yunet`): the corpus path should not depend on a default either way.

Version note: reconciled 2026-09-20 — `go.mod` now requires
`trackid-vision v0.3.0`, matching [ROADMAP.md](ROADMAP.md). The terms above were
read against that version; re-read them if it moves again.

## Geographic data (world/data/rio)

Built by `trackid-sim fetch-isp` and `fetch-banks`; see [OCCURRENCE.md](OCCURRENCE.md).

### ISP-RJ: occurrence counts and CISP boundaries

- **Files**: `BaseDPEvolucaoMensalCisp.csv` and `CISPshp.rar` from
  ispdados.rj.gov.br/Arquivos/ → `isp_counts.json`, `cisp.geojson`.
- **Licence**: **none stated.** Checked 2026-10-12: the data page, the files'
  metadata (the shapefile credits "ISP-RJ, SEPOL, SEPM") and ISP's *Plano de
  Dados Abertos* (2025) name no licence. Public government data published for
  download; we cite ISP-RJ as the source and say it is not responsible for our
  sums. **Gate before release**: ask ISP or find the state's open-data terms.

### Banco Central: bank branch register

- **Source**: BCB open data, dataset `agencias` (Olinda OData `Informes_Agencias`).
- **Licence**: **ODbL** (Open Data Commons Open Database License), as stated in
  the portal's dataset metadata, read 2026-10-12.

### OpenStreetMap via Nominatim: branch coordinates

- **Licence**: **ODbL**; credit "© OpenStreetMap contributors". The public
  Nominatim instance's usage policy (≤ 1 request/s, identifying User-Agent,
  results cached, no bulk or autocomplete) was followed: 483 branches,
  once, cached in `out/cache/nominatim.jsonl`.
- **Consequence**: `bank_branches.json` is a derived database of two ODbL
  sources. Redistributing it (the corpus) means offering it under the ODbL with
  both attributions — compatible with the release, but it must be stated.

## Rejected, and why

Recorded so the reasoning is not re-litigated, and so reviewers can see the
choice was deliberate.

| Rejected | Why |
|---|---|
| FairFace, LFW, UTKFace, FFHQ | Datasets of **real people**. They would appear as offenders in a published corpus — LGPD sensitive data — and their licences are weaker than they look. |
| DigiFace1M | Synthetic, but under a non-commercial agreement whose "Results" clause could reach whatever is built from it. |
| FLUX.1-dev, FLUX.1-Kontext-dev | Non-commercial licence. Fails the app track, and its outputs carry the restriction. |
| InfiniteYou | CC-BY-NC. |
| Identity adapters built on InsightFace (incl. SCRFD) | InsightFace models are non-commercial. |

## How we describe the result

The exact wording matters and is fixed here so every manuscript uses it:

> No real individual is represented in this corpus.

We say that, and **not** that no real data was involved. The generators were
trained on scraped photographs; claiming otherwise would be false. The
distinction is that no identifiable person is depicted or enrolled, not that the
pipeline is untainted by real-world imagery.

Also carried in every manuscript, from the PLAN principles: synthetic data
cannot show absolute field error rates, and cannot show demographic bias — the
generators carry their own skew.

## Before release (block E)

- [ ] Re-read every licence above; fill the *not yet read* entries.
- [ ] Confirm each generator's terms allow redistributing its **outputs**.
- [ ] Ship model versions, seeds and manifests with the corpus.
- [ ] If in doubt about any image: release the generator and manifests, not the
      images.
