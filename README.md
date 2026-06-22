# ssh_known_hosts_go

Port Go de [ssh_known_hosts_shell](https://github.com/vignemail1/ssh_known_hosts_shell) — binaire statique à déclarer comme `KnownHostsCommand` dans `~/.ssh/config`.

Il télécharge un fichier `known_hosts` centralisé en HTTPS, le met en cache localement avec gestion ETag / Last-Modified, et renvoie uniquement les entrées correspondant à l'hôte demandé par SSH. Le cache local assure la continuité de service hors ligne.

## Pourquoi Go plutôt que shell

- Binaire statique, sans dépendance sur `curl`, `awk`, `getent` ni `mktemp`
- Timeout HTTP natif et gestion correcte des signaux
- Verrou concurrent via `os.Mkdir` — atomique sur Linux, macOS et BSD
- Compilation croisée triviale (`GOOS`, `GOARCH`) pour distribuer sur un parc hétérogène

## Installation

```shell
# depuis les sources
git clone https://github.com/vignemail1/ssh_known_hosts_go
cd ssh_known_hosts_go
go build -o knownhosts-command-cache .
install -m 0755 knownhosts-command-cache /usr/local/bin/
```

## Configuration SSH

```sshconfig
Host *
  UserKnownHostsFile ~/.ssh/known_hosts
  KnownHostsCommand /usr/local/bin/knownhosts-command-cache %H %p
  StrictHostKeyChecking ask
```

Si le serveur SSH cible est déjà présent dans la base centralisée, aucune confirmation n'est demandée. En cas d'empreinte divergente la connexion est toujours refusée.

## Variables d'environnement

| Variable | Défaut | Description |
|---|---|---|
| `KNOWNHOSTS_URL` | URL du dépôt shell | URL HTTPS du fichier `known_hosts` centralisé |
| `KNOWNHOSTS_CACHE_DIR` | `~/.cache/ssh-knownhosts` | Répertoire de cache local |
| `KNOWNHOSTS_CACHE_TTL` | `86400` | Durée de validité du cache (secondes) |
| `KNOWNHOSTS_CONNECT_TIMEOUT` | `5` | Timeout de connexion TCP (secondes) |
| `KNOWNHOSTS_MAX_TIME` | `20` | Durée maximale de la requête HTTP (secondes) |
| `KNOWNHOSTS_RESOLVE_IPS` | `1` | Résolution DNS locale pour matcher aussi par IP (`0` pour désactiver) |

## Modèle de confiance

Le binaire complète `~/.ssh/known_hosts` sans l'écraser. La sécurité repose sur la protection HTTPS de la source centrale et les permissions `0600` appliquées aux fichiers de cache. Ce mécanisme améliore la cohérence du parc, mais ne remplace pas une gouvernance correcte des clés hôtes.

Voir aussi : [ssh_known_hosts_shell](https://github.com/vignemail1/ssh_known_hosts_shell)
