FROM postgres:18.7@sha256:64c8b0d31e4ab6ea665e8426ea406a94bf52de3c05b3a4a8bf713704eefa9328

# POSTGRES_PASSWORD is deliberately not baked in: supply it at run time.
ENV POSTGRES_USER=default_user
ENV POSTGRES_DB=meowbot

COPY --chown=postgres:postgres apps/database/meowbotdb/src/ /docker-entrypoint-initdb.d/

# The official entrypoint supports starting as the postgres user directly.
USER postgres

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=5 \
  CMD pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" || exit 1
