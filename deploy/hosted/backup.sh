#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")"
destination="${1:?Provide a new backup directory outside the deployment directory}"
if [[ -e "$destination" ]]; then
  echo "Backup destination already exists" >&2
  exit 1
fi
mkdir -p -- "$destination"
destination="$(cd -- "$destination" && pwd)"
deployment="$(docker compose exec -T hosted /usr/local/bin/wia-hosted -config /etc/wia/hosted.json -command deployment-id)"
if [[ ! "$deployment" =~ ^[a-f0-9]{24}$ ]]; then
  echo "Invalid deployment identity" >&2
  exit 1
fi
printf '%s\n' "$deployment" > "$destination/deployment-id.txt"
docker compose stop hosted
running="$(docker ps --filter "label=wia.deployment=$deployment" --format '{{.Names}}')"
if [[ -n "$running" ]]; then
  echo "Workers are still running; backup aborted. Keep the service stopped and inspect ownership." >&2
  exit 1
fi
docker volume inspect wia-hosted-state > "$destination/controller-volume.json"
docker run --rm --network none --mount type=volume,source=wia-hosted-state,target=/data,readonly alpine:3.22 tar -C /data -czf - . > "$destination/controller.tgz"
docker volume ls --filter "label=wia.deployment=$deployment" --format '{{.Name}}' > "$destination/volumes.txt"
while IFS= read -r volume; do
  [[ -z "$volume" ]] && continue
  if [[ ! "$volume" =~ ^wia-usr_[a-f0-9]{32}-data$ ]]; then
    echo "Unexpected worker volume name" >&2
    exit 1
  fi
  docker volume inspect "$volume" > "$destination/$volume.json"
  docker run --rm --network none --mount "type=volume,source=$volume,target=/data,readonly" alpine:3.22 tar -C /data -czf - . > "$destination/$volume.tgz"
done < "$destination/volumes.txt"
(cd -- "$destination" && sha256sum ./*.tgz > SHA256SUMS)
echo "Backup complete. Retain the deployment config and account secret separately, then copy this directory off the server."
docker compose start hosted
