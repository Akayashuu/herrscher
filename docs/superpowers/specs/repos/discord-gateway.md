# discord-gateway — spec

**Module :** `github.com/Akayashuu/herrscher-discord-gateway` · **Catégorie :** gateway · **Phase :** 1

## Rôle

L'**adaptateur** qui branche Discord (via `dctl`) sur la plateforme : il traduit
entre l'API Discord et les contrats Herrscher, et publie/s'abonne sur NATS. C'est
*lui* le plugin gateway ; `dctl` en est la fondation.

## Périmètre

- **Entrant :** écoute Discord (via `dctl`), publie `InboundMessage` sur
  `msg.in.discord`.
- **Sortant :** s'abonne à `msg.out.<conv>` / `progress.<session>`, exécute les
  `OutboundAction` (post, reply, react, menu, typing) via `dctl`.
- **Capacités :** annonce ce que Discord sait faire (`reactions`,
  `select-menus`, `threads`, `attachments`) dans son **manifeste**. Les actions
  non supportées sont dégradées par le core, pas ici.
- **Enregistrement :** se connecte au bus → auto-découvert par le host (aucune
  adresse codée côté core).
- Réintègre le domaine Discord sorti de `dctl` : catalogue de commandes
  (`/session`…), convention `ChoiceCustomID`, routage des clics de select-menu.

**Ne fait pas :** aucune logique de session/agent (c'est `core`). Pur transport
+ traduction + capacités.

## Dépendances

`dctl` (outil pur) + `contracts` (ABI). Tire le SDK/contrats — ce qui justifie de
le garder **séparé** de `dctl`.

## Phase 1

Premier plugin extrait en process sur NATS ; valide le **contrat gateway** de
bout en bout. Avant ça (Phase 0), son rôle est tenu in-process par l'adapter
`Gateway` dans `core`.

## Points ouverts

- Découpage exact gateway vs core pour les **interactions** (slash-commands) :
  qui possède le catalogue, qui route les réponses.
