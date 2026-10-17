"""Pick a reranker on evidence.

Scores every candidate cross-encoder over the hand-written corpus beside this file, and
prints how each does against the embedding model alone. Run it when choosing or changing
a reranker; the numbers it printed when the choice was made are recorded in
ml/embed/README.md.

    python ml/embed/eval/rank.py

It measures the judgement of whoever wrote the corpus, not a customer's, so treat it as a
guard against a model being worse rather than proof of how good one is.
"""

from __future__ import annotations

import json
import time
from pathlib import Path

from fastembed import TextEmbedding
from fastembed.rerank.cross_encoder import TextCrossEncoder

EMBED_MODEL = "BAAI/bge-small-en-v1.5"

# Every cross-encoder fastembed offers under a licence this product may use. The
# multilingual jina v2 is cc-by-nc-4.0 and is therefore not a candidate at all.
CANDIDATES = [
    "Xenova/ms-marco-MiniLM-L-6-v2",
    "Xenova/ms-marco-MiniLM-L-12-v2",
    "jinaai/jina-reranker-v1-tiny-en",
    "jinaai/jina-reranker-v1-turbo-en",
]


def load():
    data = json.loads((Path(__file__).parent / "corpus.json").read_text(encoding="utf-8"))
    ids = list(data["passages"])
    return ids, [data["passages"][i] for i in ids], data["questions"]


def score(ranked_per_question, questions):
    """Mean reciprocal rank, recall@3, and how often the top hit is a right one."""
    rr = hits = top1 = 0.0
    for ranked, q in zip(ranked_per_question, questions):
        want = set(q["want"])
        first = next((i for i, pid in enumerate(ranked) if pid in want), None)
        if first is not None:
            rr += 1 / (first + 1)
        if any(pid in want for pid in ranked[:3]):
            hits += 1
        if ranked and ranked[0] in want:
            top1 += 1
    n = len(questions)
    return rr / n, hits / n, top1 / n


def main() -> None:
    ids, texts, questions = load()
    embedder = TextEmbedding(model_name=EMBED_MODEL)
    vectors = list(embedder.embed(texts))

    # The baseline: the embedding model on its own, which is what the notebook's
    # meaning-matching half does today.
    base = []
    for q in questions:
        qv = list(embedder.query_embed([q["q"]]))[0]
        order = sorted(range(len(ids)), key=lambda i: -float(qv @ vectors[i]))
        base.append([ids[i] for i in order])
    mrr, r3, t1 = score(base, questions)
    print(f"{'embedding alone':45s} MRR {mrr:.3f}  recall@3 {r3:.3f}  top-1 {t1:.3f}")

    for name in CANDIDATES:
        try:
            ce = TextCrossEncoder(model_name=name)
        except Exception as e:  # a model that will not load is not a candidate
            print(f"{name:45s} did not load: {e}")
            continue
        ranked, started = [], time.perf_counter()
        for q in questions:
            scores = list(ce.rerank(q["q"], texts))
            order = sorted(range(len(ids)), key=lambda i: -scores[i])
            ranked.append([ids[i] for i in order])
        each = (time.perf_counter() - started) / len(questions)
        mrr, r3, t1 = score(ranked, questions)
        print(f"{name:45s} MRR {mrr:.3f}  recall@3 {r3:.3f}  top-1 {t1:.3f}  {each*1000:.0f} ms/question over {len(ids)} passages")


if __name__ == "__main__":
    main()
