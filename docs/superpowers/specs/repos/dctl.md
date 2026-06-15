# dctl — spec

**Module :** `github.com/Akayashuu/dctl` · **Catégorie :** outil standalone · **Phase :** 0

## Rôle

Le **client/outil Discord pur**, utilisable **seul**, sans rien connaître de
Herrscher (ni core, ni bus, ni plugins). C'est la fondation réutilisable sur
laquelle s'appuie `discord-gateway`.

Repo **transféré** depuis `vskstudio/dctl`, puis **allégé**.

## Périmètre (après nettoyage)

**Garde :** client REST Discord (`Send`, `Reply`, `Read`, channels, threads,
reactions), plomberie d'interactions générique (parsers, `RegisterCommands(ctx,
catalogue)`, `RespondInteraction`, autocomplete…), composants génériques
(`SelectOption`, envoi de menu générique). CLI `dctl` (`send`, `bridge`, …).

**Retire (part dans `core` / `discord-gateway`) :**
- tout le **domaine session** : `internal/bridge`, `handler`, `supervisor`,
  `serve`, `gateway`, `state`, `config`, `session`, `control`, `worktree`,
  `forge`, `service`… → migrent dans `core`.
- les **fuites domaine** du package client : `dctlCommands()` (catalogue
  `/session`…), `ChoiceCustomID`/`ParseChoiceCustomID` → remontent côté app
  (`discord-gateway`/`core`). `RegisterCommands` devient paramétré.

**Invariant :** `dctl` ne dépend ni de `contracts`, ni de `core`, ni du SDK
plugin. Jamais. C'est ce qui garantit « utilisable sans le core ».

## Conventions

Code propre, **pas de commentaires inutiles** — on nettoie le style verbeux
actuel au passage (longs doc-comments sur presque chaque fonction).

## Dépendances

Aucune (lib standard + HTTP Discord).

## Points ouverts

- Renommer le package `dctl` → `discord` ? (clarté vs churn d'imports — tranché
  plus tard, hors socle.)
