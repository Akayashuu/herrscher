# Herrscher — Plateforme-agent à plugins (design)

> Statut : design validé en brainstorm (2026-06-15). Vision « à terme ».
> Ce document fige l'architecture cible et la feuille de route. Chaque phase
> aura ensuite son propre spec → plan → implémentation.

## 1. Intention

**Herrscher** est une plateforme-agent IA **auto-hébergée, modulaire et
multi-canal**. Elle vit dans les messageries de l'utilisateur (Discord
aujourd'hui, Telegram et autres demain), y tient des **sessions** conversationnelles
pilotant un agent (Claude), et s'étend par **plugins** qu'on choisit à
l'installation.

Principes directeurs (par ordre de priorité) :

1. **Tout est plugin, opt-in à l'install.** On compose son instance : telle
   gateway, tel backend, telle mémoire, avec ou sans orchestrateur.
2. **Standalone d'abord.** Chaque brique est un outil autonome, utilisable
   **sans** le core. L'intégration à la plateforme vit dans un *adaptateur*
   séparé (ports & adapters). Exemple : `dctl` reste un client Discord pur,
   utilisable seul.
3. **Polyrepo.** Un dépôt par composant, versionné et releasé indépendamment.
4. **Frontières typées, pas conventionnelles.** Les contrats inter-composants
   sont des `.proto` / interfaces, compilés et semver-és — pas des conventions
   implicites.

## 2. Modèle conceptuel

- **Conversation** — handle abstrait `{ gateway, id }` (ex. `{discord, <channelID>}`),
  **opaque** pour le core. Une session n'est *pas* « un channel Discord » : elle
  est liée à une *conversation* qu'une gateway sait résoudre.
- **Session** — une conversation vivante pilotée par un backend (l'agent). Le
  core en possède le cycle de vie et le mapping session ↔ conversation.
- **Capacités** — chaque plugin **annonce** ce qu'il sait faire (`reactions`,
  `select-menus`, `threads`, `attachments`…). Le core émet des actions en
  **best-effort** : un plugin qui ne sait pas faire une action la **dégrade**
  proprement (ex. Telegram : menu → liste numérotée). Le core ne connaît jamais
  Discord ou Telegram en dur. C'est le mécanisme qui rend la plateforme
  réellement agnostique, et la généralisation des anciennes fuites domaine
  (`SendChoiceMenu`, réactions emoji, select-menus) du client Discord.

## 3. Frontière du noyau

**Core / noyau (toujours présent) :**

- **host** — config, **sélection des plugins** à l'install, câblage du bus NATS
  + registre gRPC, cycle de vie / health des plugins.
- **session-manager** — conversations ↔ sessions, allowlist/auth, routage
  entrant → backend, events backend → sortant, appels memory & orchestrator.
  C'est le module qui *fait fonctionner* l'ensemble : il est dans le core, **non
  pluggable**.

Tout le reste est plugin.

## 4. Catégories de plugins

| Catégorie | Exemples | Rôle |
|---|---|---|
| **gateway** | discord (`dctl`), telegram | Adapter une plateforme de chat ; entrée/sortie de messages. |
| **backend** | `stream`, `oneshot`, `tmux` | Piloter l'agent par session : reçoit un message, rend le tour + events de progression. |
| **memory** | obsidian | Base de mémoire : stocker / interroger. |
| **orchestrator** | docker | Fournir l'environnement d'exécution d'une session (conteneur). **Optionnel** : sans lui, exécution locale (worktree). |

Décisions clés :

- **Le backend est un process à part** (plugin indépendant), pas un driver
  in-core. tmux/stream/oneshot deviennent chacun un plugin backend.
- **L'orchestrator est optionnel.** Le système tourne sans lui ; il ne fait que
  *fournir un environnement* (conteneur Docker) quand il est présent.

## 5. Transports

Découpage **par forme de trafic** :

- **NATS — plan messages** (asynchrone, fan-out, multi-gateway)
  - `msg.in.<gateway>` : `InboundMessage{ Conversation, Author, Text, Attachments[], MessageID, Ts }`
  - `msg.out.<gateway>` : `OutboundAction{ Conversation, Kind: post|reply|react|menu|typing, Payload, ReplyTo }`
  - `progress.<session>` : events intermédiaires (outils, raisonnement).
  - Un gateway s'**auto-enregistre en se connectant** au bus : le core n'a aucune
    adresse à configurer.
- **gRPC — plan services** (requête/réponse typée, plan de contrôle)
  - `Memory` : `Query`, `Store`.
  - `Orchestrator` : `StartSession`, `StopSession`, `Exec`, `Status`.

**À trancher dans le sous-spec backend :** transport backend ↔ manager. Piste
privilégiée — gRPC **sticky** (le backend est *stateful* par session : stream
`claude` persistant / pane tmux, donc une session doit rester collée à son
instance de backend), avec la **progression mirroir sur NATS** (`progress.<session>`).

## 6. Repos (polyrepo)

Hébergement : **compte GitHub `Akayashuu`** (repos publics). Les composants de la
plateforme sont préfixés **`herrscher-`** (`herrscher-core`, `herrscher-contracts`,
`herrscher-discord-gateway`, `herrscher-stream-backend`…) ; seul **`dctl`** garde
son nom nu, car c'est un outil standalone antérieur à la plateforme. Chemins de
modules Go : `github.com/Akayashuu/<repo>`.

| Repo | Rôle | Dépend de | Socle ? |
|---|---|---|---|
| **`herrscher-contracts`** | **proto-only** : enveloppes NATS, services gRPC, schéma de manifeste, modèle de capacités. ABI semver, polyglotte (zéro code applicatif Go). | — | ✅ |
| **`herrscher-core`** | host + session-manager. *(carvé hors de l'actuel `dctl`)* | `herrscher-contracts` | ✅ |
| **`dctl`** | client/outil Discord **pur**, utilisable seul. *(transféré depuis `vskstudio`, allégé)* | — | ✅ |
| **`herrscher-discord-gateway`** | adaptateur Discord → NATS. *(neuf)* | `dctl`, `herrscher-contracts` | ✅ |
| **`herrscher-tmux-backend`** | plugin backend tmux | `herrscher-contracts` | |
| **`herrscher-stream-backend`** | plugin backend claude/stream | `herrscher-contracts` | |
| **`herrscher-obsidian-memory`** | plugin memory | `herrscher-contracts` | |
| **`herrscher-docker-orchestrator`** | plugin orchestrator (opt-in) | `herrscher-contracts` | |
| *(plus tard)* **`herrscher-telegram-gateway`** | 2ᵉ gateway, prouve l'agnosticité | `herrscher-contracts` | |

**Démarrage : le socle d'abord** (`herrscher-contracts`, `herrscher-core`, `dctl`, `herrscher-discord-gateway`).
Les plugins backend/memory/orchestrator sont créés quand on les attaque.
`dctl` est **transféré** depuis `vskstudio/dctl` (l'utilisateur y est admin),
ce qui préserve historique, issues et étoiles.

Le repo **`herrscher-contracts`** est le *linchpin* : la seule source de vérité partagée.
Sa compatibilité ascendante (semver + modèle de capacités comme amortisseur)
est ce qui rend le polyrepo gérable.

**Nuance pragmatique sur « adaptateur dans un repo à part ».** La séparation
outil-pur / adaptateur en **deux repos** a surtout du sens pour `dctl`, qui
existe déjà comme outil pur réutilisable. Les plugins **greenfield** (backends,
memory, orchestrator) peuvent **naître en un seul repo plugin** et n'extraire un
cœur pur que si un besoin de réutilisation apparaît — pour ne pas créer 14 repos
pour 7 plugins.

## 7. Feuille de route incrémentale

Principe de dé-risquage : **les contrats (ports) d'abord, le réseau ensuite.**
On définit les ports en Go *à l'intérieur du monolithe actuel*, sans réseau ; le
jour où on éclate en process, on rebranche les *mêmes* contrats sur NATS/gRPC.
Chaque phase laisse un système fonctionnel.

| Phase | Quoi | Livrable |
|---|---|---|
| **0 — Frontières in-process** | Extraire les ports `Gateway`, `Backend`, `Memory`, `Orchestrator` (interfaces Go). Discord/tmux/stream/file-memory/worktree deviennent des implémentations derrière ces ports. **Dégager `dctl`** du domaine session (core → futur repo `core`) et de ses fuites. Introduire l'abstraction **Conversation** + le modèle de **capacités**. | 1 binaire, mais cœur agnostique + `dctl` redevenu client Discord pur. |
| **1 — dctl standalone + 1ʳᵉ gateway NATS** | Introduire NATS. `gateway-discord` devient un process qui publie/s'abonne ; le session-manager consomme le bus. | Valide le contrat **gateway**. |
| **2 — backends en process** | `stream`/`oneshot`/`tmux` deviennent des plugins-process ; trancher le transport backend. | Valide le contrat **backend**. |
| **3 — memory-obsidian (gRPC)** | Remplacer la mémoire fichier par un service Memory. | Valide le contrat **memory**. |
| **4 — orchestrator-docker (gRPC, opt-in)** | worktree → conteneur par session. | Valide le contrat **orchestrator** + « marche sans lui ». |
| **5 — gateway-telegram** | 2ᵉ gateway. | **Prouve** l'agnosticité de bout en bout. |

Le **host / manifeste / sélection à l'install** s'introduit progressivement :
minimal en Phase 1, formalisé à mesure que le nombre de plugins grandit.

➜ **La Phase 0 est la première pierre** : essentiellement du refactor de
l'existant, plus haut levier, débloque tout le reste.

## 7b. Conventions de code

- **Pas de commentaires inutiles.** Le code se suffit à lui-même ; on ne garde
  que les commentaires à vraie valeur (pourquoi non-évident, invariants, pièges).
- Cette règle s'applique aussi au **`dctl` existant**, aujourd'hui sur-commenté
  (longs doc-comments sur presque chaque fonction) : on **nettoie au passage**,
  on ne reproduit pas ce style verbeux.

## 8. Décisions actées (récap)

- Nom de la plateforme : **Herrscher**.
- Plateforme à **plugins**, **polyrepo**, services en **process séparés**,
  **opt-in à l'install**.
- **Standalone d'abord** + **adaptateur dans un repo séparé** (ports & adapters).
- **Core (toujours présent)** = host + session-manager.
- Catégories de plugins : **gateway**, **backend**, **memory**,
  **orchestrator** (opt-in).
- **Backend = process à part** ; **orchestrator optionnel**.
- **Conversation** abstraite (≠ channel) + **capacités** (dégradation gracieuse).
- Transport : **NATS** (messages) + **gRPC** (services) ; backend transport à
  trancher (piste : gRPC sticky + progression NATS).
- Repo **`herrscher-contracts`** = **proto-only**, ABI semver.
- **`dctl` conservé** comme base de la gateway Discord (outil pur), `core`
  carvé hors de lui.
- Tous les repos sous le **compte GitHub `Akayashuu`** (`github.com/Akayashuu/<repo>`) ;
  `dctl` **transféré** depuis `vskstudio/dctl`. On crée le **socle d'abord**.
- **Style de code : pas de commentaires inutiles**, on nettoie aussi le `dctl`
  existant (sur-commenté).

## 9. Points ouverts (à trancher dans les sous-specs)


- Transport exact backend ↔ manager (gRPC sticky vs NATS work-queue à affinité).
- Format précis du **manifeste** de plugin et mécanisme de **découverte/registre**.
- Schéma de **sélection à l'install** (CLI `herrscher plugin add …` ? fichier de
  composition ?).
- Stratégie de **versioning/compat** du repo `herrscher-contracts` (politique semver,
  fenêtre de compat des capacités).
- Relation fine **backend ↔ orchestrator** quand les deux sont présents (le
  backend tourne-t-il *dans* le conteneur fourni par l'orchestrator ?).
