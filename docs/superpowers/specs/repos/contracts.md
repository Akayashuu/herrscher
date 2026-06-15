# contracts — spec

**Module :** `github.com/Akayashuu/herrscher-contracts` · **Catégorie :** socle (ABI) · **Phase :** 0→1

## Rôle

La **source de vérité partagée** de la plateforme : les contrats inter-composants,
versionnés en semver. C'est l'ABI dont **tout** plugin dépend. Rien d'autre n'est
partagé entre repos.

## Périmètre

**Contient (proto-only) :**
- **gRPC** (`.proto`) — services du plan de contrôle : `Memory` (`Query`, `Store`),
  `Orchestrator` (`StartSession`, `StopSession`, `Exec`, `Status`).
- **Enveloppes NATS** — schémas des messages du plan messages : `InboundMessage`,
  `OutboundAction` (`Kind: post|reply|react|menu|typing`), `ProgressEvent`. Plus
  la convention de **sujets** (`msg.in.<gateway>`, `msg.out.<gateway>`,
  `progress.<session>`).
- **Manifeste de plugin** — schéma déclaratif : type/catégorie, version d'ABI
  ciblée, **capacités** annoncées, schéma de config.
- **Modèle de capacités** — énumération + sémantique de dégradation.

**Ne contient pas :** aucun code applicatif Go, aucun helper d'enregistrement,
aucune logique. Proto + schémas uniquement → polyglotte, réutilisable hors Go.

## Versioning

- Semver strict. La compat ascendante est l'invariant clé du polyrepo.
- Le **modèle de capacités** est l'amortisseur : un plugin négocie ce qu'il sait
  faire plutôt que de casser sur un champ inconnu.
- Chaque plugin déclare l'ABI qu'il vise (`contracts vX.Y`).

## Dépendances

Aucune.

## Points ouverts

- Code généré (Go) commité dans `contracts` ou généré côté consommateur ?
- Politique de dépréciation des champs/sujets.
