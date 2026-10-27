"""Does retrieval need a polarity channel, and is a wave representation the way to add one?

Asked because of arXiv 2509.09691, "Wave-Based Semantic Memory with Resonance-Based
Retrieval", which argues that cosine conflates a statement with its negation and proposes
storing meaning as a complex waveform scored by phase-aware interference.

The paper's own compatibility path is a no-op here, and its Theorem 5 says why: under the
sign-phase mapping a pattern is purely real, and with equal norms the resonance score is
exactly (1 + cos)/2. Our vectors come out of fastembed with norm 1, so that reduction is
exact -- measured on the ordinary corpus, resonance reorders nothing at all, with the
largest gap between the two scores at 9e-15.

What survives is narrower and real: one scalar phase per passage, which under unit norms
turns equation 5 into S = (1 + cos(dphi) cos(theta)) / 2, topical similarity scaled by
polarity agreement. That needs no phase-producing encoder and no second datastore, so it
is the only form of the claim this product could adopt. This script measures it against
cosine and against two cruder ways of spending the same signal, on both corpora.

    python ml/embed/eval/polarity.py

Both corpora are hand-written, so these measure the judgement of whoever wrote them. The
polarity corpus in particular was built to make polarity decisive, which makes it a fair
test of whether the channel helps and a poor estimate of how often it would fire.

THE CHANNEL DOES NOT SURVIVE REAL DOCUMENTS, and the 0.905 below should not be read as a
reason to wire it in. Measured over 147 passages chunked by the product's own splitter
from ten real editorial guidance pages (spikes/04-polarity regenerates them), the loose
detector flags 85% of passages and the strict one 67%, so the flag separates almost
nothing; the dominant match is a bare "not" doing non-directive work ("you do not need to
sign up", "Not 'front-end'", "the content might stop being useful"). Worse, and
structurally, 85% of strictly-flagged passages ALSO carry a prescription, because a
900-character chunk of a rules document holds several rules at once. One scalar phase per
passage assumes a passage has one polarity, and a real one does not. The score below is
an artefact of a corpus with exactly one rule and one clean marker per passage.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

import numpy as np
from fastembed import TextEmbedding
from fastembed.rerank.cross_encoder import TextCrossEncoder

EMBED_MODEL = "BAAI/bge-small-en-v1.5"
RERANKER = "Xenova/ms-marco-MiniLM-L-6-v2"

# Deterministic and lexical on purpose: a per-item path in this product may not make a
# model call, so a polarity channel that needed one could not ship whatever it scored.
FORBID = re.compile(
    r"\b(never|not|don't|do not|cannot|can't|won't|must not|should not|shouldn't|"
    r"avoid|forbidden|banned|prohibited|off limits|refrain|stop|no longer)\b",
    re.I,
)


def phase(text: str) -> float:
    """pi for a prohibition, 0 for a prescription."""
    return np.pi if FORBID.search(text) else 0.0


def load(name: str):
    d = json.loads((Path(__file__).parent / name).read_text(encoding="utf-8"))
    ids = list(d["passages"])
    return ids, [d["passages"][i] for i in ids], d["questions"]


def measure(ids, questions, orders):
    """Mean reciprocal rank and how often the top hit is a right one."""
    rr = top1 = 0.0
    for order, q in zip(orders, questions):
        want = set(q["want"])
        first = next((i for i, j in enumerate(order) if ids[j] in want), None)
        if first is not None:
            rr += 1 / (first + 1)
        if ids[order[0]] in want:
            top1 += 1
    return rr / len(questions), int(top1)


def run(embedder, cross, name: str, label: str) -> None:
    ids, texts, questions = load(name)
    vectors = np.array(list(embedder.embed(texts)), dtype=np.float64)
    passage_phase = np.array([phase(t) for t in texts])

    scored: dict[str, list] = {k: [] for k in
                               ("cosine alone", "resonance, polarity as phase",
                                "flat penalty on disagreement", "drop the disagreeing half",
                                "cross-encoder (what we ship)")}
    for q in questions:
        qv = np.array(list(embedder.query_embed([q["q"]]))[0], dtype=np.float64)
        cos = vectors @ qv
        # +1 where the question and the passage share a polarity, -1 where they differ.
        agree = np.cos(phase(q["q"]) - passage_phase)
        scored["cosine alone"].append(cos)
        scored["resonance, polarity as phase"].append((1 + agree * cos) / 2)
        scored["flat penalty on disagreement"].append(cos - 0.15 * (agree < 0))
        scored["drop the disagreeing half"].append(np.where(agree < 0, cos - 10, cos))
        scored["cross-encoder (what we ship)"].append(np.array(list(cross.rerank(q["q"], texts))))

    prohibitions = int((passage_phase > 0).sum())
    print(f"\n{label}")
    print(f"  {len(ids)} passages, {prohibitions} read as prohibitions, {len(questions)} questions")
    for variant, per_question in scored.items():
        orders = [sorted(range(len(ids)), key=lambda i: -s[i]) for s in per_question]
        mrr, top1 = measure(ids, questions, orders)
        print(f"  {variant:32s} MRR {mrr:.3f}  top-1 {top1} of {len(questions)}")


def main() -> None:
    embedder = TextEmbedding(model_name=EMBED_MODEL)
    cross = TextCrossEncoder(model_name=RERANKER)
    run(embedder, cross, "corpus.json",
        "Ordinary corpus: polarity is not what separates these, so this is the regression check.")
    run(embedder, cross, "polarity.json",
        "Polarity corpus: every topic appears as a rule and as a prohibition, so only polarity separates them.")


if __name__ == "__main__":
    main()
