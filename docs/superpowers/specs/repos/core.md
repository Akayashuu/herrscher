# core — spec

**Module :** `github.com/Akayashuu/herrscher-core` · **Catégorie :** noyau · **Phase :** 0→1

## Rôle

Le **noyau toujours présent** de Herrscher. Deux responsabilités :

- **host** — config, **sélection des plugins** à l'install, câblage du bus NATS
  + registre gRPC, cycle de vie & health des plugins.
- **session-manager** — conversations ↔ sessions, allowlist/auth, routage
  entrant → backend, events backend → sortant, appels memory & orchestrator.

C'est le module qui *fait fonctionner* l'ensemble ; il n'est **pas** un plugin.
Le code actuel du monolithe dctl (`internal/bridge`, `handler`, `supervisor`,
`serve`, `gateway`, `state`, `config`…) migre ici, derrière les **ports**.

## Périmètre

- Définit/consomme les **ports** : `Gateway`, `Backend`, `Memory`,
  `Orchestrator` (interfaces Go alignées sur `contracts`).
- Possède l'abstraction **Conversation** (`{gateway, id}`) et le mapping
  session ↔ conversation.
- Émet des **OutboundAction** en best-effort ; s'appuie sur les **capacités**
  annoncées des gateways pour la dégradation.
- Allowlist / autorisation par session.

**Ne fait pas :** ne connaît aucune plateforme de chat en dur (pas de Discord),
ni aucun backend concret, ni le stockage mémoire. Tout passe par les ports.

## Dépendances

`contracts`. (En Phase 0, les implémentations vivent encore in-process ; en
Phase 1+, le core ne parle aux plugins que via NATS/gRPC.)

## Phase 0 (in-process)

Extraire les ports et faire des implémentations actuelles (discord/tmux/stream/
file-memory/worktree) des adapters derrière ces ports, **sans réseau**. Un seul
binaire, mais cœur agnostique. C'est le carve-out de `core` hors de `dctl`.

## Points ouverts

- Frontière exacte host vs session-manager (un binaire, deux paquets ?).
- Transport backend ↔ manager (cf. spec backend).
