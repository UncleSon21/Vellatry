# Embedding service

Turns short marketing texts into vectors so Vellatry can tell which of them mean the
same thing: two proposed topics that are the same topic in different words, or a Search
Console query that belongs to a topic you already track.

It is **inference, not training**. The model is pretrained and pinned; nothing here
learns from customer data. Vellatry trains nothing until there are enough labelled
decisions to beat its rules (decisions 50 and 55).

| | |
| --- | --- |
| Model | `BAAI/bge-small-en-v1.5`, 384 dimensions, MIT licence |
| Runtime | [fastembed](https://github.com/qdrant/fastembed) (ONNX, Apache 2.0). No PyTorch: the image stays small and runs on a shared CPU |
| Interface | `POST /embed {"texts": [...]}` → `{"model", "dim", "vectors"}`, and `GET /healthz` |
| Callers | The worker only, through `internal/embed`. The api never calls it |
| Network | Fly private network (Flycast). No public address, so no auth of its own |

Vectors are comparable only with others from the same model, so every response names
the model, the name is stored beside each vector, and `internal/embed` refuses to
compare across models. Changing `requirements.txt` or the model means re-embedding
everything.

## Run it locally

```bash
pip install -r ml/embed/requirements.txt
```

```bash
python -m uvicorn app:app --port 8000 --app-dir ml/embed
```

Then point the worker at it:

```bash
EMBED_URL=http://localhost:8000
```

The first start downloads the weights (about 130 MB) to the fastembed cache; the
Docker image bakes them in instead.

## Deploy

See the header of `ml/embed/fly.toml`. It sleeps when idle and wakes on the worker's
first request, so it costs nothing between runs.
