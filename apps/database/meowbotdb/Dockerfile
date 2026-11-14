FROM postgres:18.7@sha256:9f4fd4aa918f52ae3d6f3b687f1de90d8f867cd4d520021545a9a50c120eae3b

# POSTGRES_PASSWORD is deliberately not baked in: supply it at run time.
ENV POSTGRES_USER=default_user
ENV POSTGRES_DB=meowbot

COPY --chown=postgres:postgres apps/database/meowbotdb/src/ /docker-entrypoint-initdb.d/

# The official entrypoint supports starting as the postgres user directly.
USER postgres

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=5 \
  CMD pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" || exit 1
