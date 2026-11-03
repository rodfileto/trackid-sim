"""Fetch only what the Qwen-Image-Edit spike needs.

The 38 GB fp16 transformer/ is deliberately skipped: it is replaced by a 12 GB
GGUF quantization, without which this does not fit in 60 GB of RAM alongside a
desktop. Everything else (the Qwen2.5-VL text encoder, VAE, tokenizer,
processor, scheduler) comes from the original repo.
"""
from huggingface_hub import hf_hub_download, snapshot_download

gguf = hf_hub_download("QuantStack/Qwen-Image-Edit-GGUF", "Qwen_Image_Edit-Q4_K_M.gguf")
print("gguf ->", gguf, flush=True)
rest = snapshot_download("Qwen/Qwen-Image-Edit", ignore_patterns=["transformer/*"])
print("rest ->", rest, flush=True)
print("done", flush=True)
