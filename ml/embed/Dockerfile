# The embedding service. The model weights are downloaded at build time, so a machine
# that has just started answers its first request without fetching anything.
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    FASTEMBED_CACHE_PATH=/opt/models \
    HF_HUB_DISABLE_TELEMETRY=1

WORKDIR /srv

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py ./

# Bake the weights into the image and fail the build if the model cannot be fetched,
# rather than discovering it on the first request in production.
RUN python -c "from app import model; model(); print('model cached')"

RUN useradd --create-home --uid 10001 vellatry && chown -R vellatry /opt/models
USER vellatry

EXPOSE 8000
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1"]
