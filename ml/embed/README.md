# Embedding service

Turns short marketing texts into vectors so Vellatry can tell which of them mean the
same thing: two proposed topics that are the same topic in different words, or a Search
Console query that belongs to a topic you already track. It also scores a question
against the passages a notebook has shortlisted, so the ones that answer it come first.

It is **inference, not training**. The model is pretrained and pinned; nothing here
learns from customer data. Vellatry trains nothing until there are enough labelled
decisions to beat its rules (decisions 50 and 55).

| | |
| --- | --- |
| Model | `BAAI/bge-small-en-v1.5`, 384 dimensions, MIT licence |
| Reranker | `Xenova/ms-marco-MiniLM-L-6-v2`, Apache 2.0, 80 MB (chosen by measurement, below) |
| Runtime | [fastembed](https://github.com/qdrant/fastembed) (ONNX, Apache 2.0). No PyTorch: the image stays small and runs on a shared CPU |
| Interface | `POST /embed {"texts": [...]}` → `{"model", "dim", "vectors"}`, `POST /rerank {"query", "passages": [...]}` → `{"model", "scores"}`, and `GET /healthz` |
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

On Windows, leave `FASTEMBED_CACHE_PATH` unset. Hugging Face's cache nests long blob
names, and under a deep folder the path passes 260 characters and the download fails
with a confusing `FileNotFoundError`. The default cache sits directly under the temp
directory, which is short enough. (fastembed also tries its own mirror first, which
currently answers 403; it then falls back to Hugging Face, which works.)

## Deploy

See the header of `ml/embed/fly.toml`. It sleeps when idle and wakes on the worker's
first request, so it costs nothing between runs.

## The reranker, and why this one

An embedding turns the question and a passage into vectors separately and compares them.
That is fast enough to run over a whole notebook and blunt: the question is never read
beside the passage. A cross-encoder reads the two together and scores the pair, which is
much better at telling an answer from something merely on the same subject, and much too
slow to run over everything. So the Go side searches first and sends only the shortlist.

The model was chosen by measuring, not by reputation. `ml/embed/eval/rank.py` scores
every cross-encoder fastembed offers under a licence this product may use, against a
hand-written corpus of the kind of passages a notebook holds and questions asked the way
a marketing team asks them. The multilingual `jina-reranker-v2` is cc-by-nc-4.0 and was
therefore never a candidate.

| | MRR | recall@3 | top-1 | per question |
| --- | --- | --- | --- | --- |
| the embedding alone | 0.692 | 0.812 | 0.500 | — |
| **Xenova/ms-marco-MiniLM-L-6-v2** | **0.794** | **0.938** | **0.688** | 44 ms |
| Xenova/ms-marco-MiniLM-L-12-v2 | 0.792 | 0.875 | 0.688 | 87 ms |
| jinaai/jina-reranker-v1-turbo-en | 0.703 | 0.938 | 0.500 | 55 ms |
| jinaai/jina-reranker-v1-tiny-en | 0.690 | 0.688 | 0.625 | 42 ms |

The winner is best on all three measures, the smallest and the fastest; L-12 costs twice
the latency for nothing. Worth noting that `jina-reranker-v1-tiny-en` scores *worse than
no reranker at all* on recall@3, which is the reason for measuring rather than picking a
name.

Measured again through the real stack — a notebook in Postgres, full-text search,
vectors and reciprocal rank fusion, then this model — reranking takes MRR from 0.679 to
0.794 and puts the right passage first on 11 of 16 questions instead of 8.
`TestLiveRerankBeatsSearchAlone` in `internal/notebook` is that measurement, and it fails
if a change to either model loses ground:

```bash
python -m uvicorn app:app --port 8099 --app-dir ml/embed
EMBED_URL=http://localhost:8099 go test ./internal/notebook/ -run Live -v
```

The corpus is somebody's idea of what should rank first rather than a customer's, so
these numbers are a guard against a change making retrieval worse, not proof of how good
retrieval is.
