# Herrscher — Phase 0a : Conversation, capacités, registre de plugins, purification dctl (design)

> Statut : design validé en brainstorm (2026-06-16). Première tranche de la
> Phase 0 du [design plateforme](2026-06-15-herrscher-plateforme-plugins-design.md).
> Tout est **in-process, un seul binaire, build vert à chaque étape, sans split
> de repo ni réseau**. Le module reste `github.com/vskstudio/dctl` (le renommage
> vers `Akayashuu` est indépendant).

## 1. Intention

Poser les **frontières plugin** dans le monolithe actuel, sans réseau. À la fin
de 0a :

- le domaine ne connaît plus Discord en dur ; il parle à un **port Gateway** ;
- une session n'est plus « un channel Discord » mais une **Conversation** opaque
  adressée par une gateway, avec une **identité de session stable** propre ;
- l'adaptateur Discord est un **plugin enregistré** via un **manifeste**, pas un
  câblage en dur dans `serve` ;
- le package racine `dctl` est redevenu un **client Discord pur**.

Cap directeur (rappel du design plateforme) : *plugin = un dossier + un CLI, on
le pose et ça marche*. Les ports de 0a sont le **brouillon du vrai contrat**
(que `herrscher-contracts` figera) — pas de simples interfaces internes de
confort.

## 2. Décisions actées (en brainstorm)

- **Découpage** : Phase 0 en tranches. 0a = Conversation + capacités + registre +
  purification dctl. (0b = port Backend ; 0c = Gateway/Orchestrator formalisés.)
- **Port Gateway = orienté méthodes** (option B), pas union d'actions. Plus
  ergonomique à appeler/étendre. Conséquence assumée (§7) : un fin pont
  « appels ↔ messages » au passage process en Phase 1.
- **Modèle de capacités introduit dès 0a**, avec dégradation **dans un
  décorateur**, jamais dans le domaine.
- **Identité de session stable séparée** : `Session.ID` généré, distinct du
  `Name` (label humain) et du `ChannelID` (adresse). Prépare le resume
  cross-canal.
- **Manifeste + mini-registre in-process dès 0a** : Discord s'enregistre comme
  plugin ; le host le découvre uniformément.
- **Hors 0a** : port Backend, Memory (aucun code mémoire n'existe), formalisation
  Orchestrator (`worktree` est déjà une interface injectable), NATS/réseau.

## 3. État actuel (cartographie)

- **Package racine `.`** = client Discord REST **pollué** par du domaine :
  - `interactions.go` — `dctlCommands()` (catalogue `/session{create|close|list|allow|who}`,
    `/workspace`, `/service`, `/set`) + wiring `RegisterCommands()`.
  - `components.go` — `ChoiceCustomID()` / `ParseChoiceCustomID()` (convention de
    `custom_id` qui encode la session pour le routage des clics).
  - `components.go` — `SendChoiceMenu()` : en réalité un **select-menu Discord
    générique**, juste nommé/utilisé côté domaine.
- **Identité session** : `internal/state/state.go` `Session.ChannelID` sert à la
  fois de **clé logique** et d'**adresse Discord** (lu par `bridge`, `handler`,
  `supervisor`).
- **Actions sortantes réellement émises** (depuis `internal/bridge/bridge.go`) :
  `react` (👀/⚠️/✅), `reply`, `send`, `menu` (select). **Pas** de `typing`,
  **pas** de threads. Donc capacités concrètes = `{reactions, select-menus, replies}`.
- **`internal/gateway`** = vraie connexion **websocket** Discord (interactions
  `INTERACTION_CREATE`), déjà isolée, appelée par `internal/serve`.
- **`internal/worktree`** = port Orchestrator de fait (interface injectable :
  `Create`/`Branch`/`Remove`).
- **Aucune mémoire** persistante hors `state.json` + journals participants.

## 4. Architecture cible 0a

### 4.1 Noyau — `kernel/`

```go
type Conversation struct { Gateway string; ID string } // opaque pour le domaine

type Capabilities struct { Reactions, SelectMenus, Replies bool }

type Choice struct { Label, Value string }
type MessageID = string

type Gateway interface {
    Manifest() Manifest
    Capabilities() Capabilities
    Post(ctx context.Context, conv Conversation, text string) (MessageID, error)
    Reply(ctx context.Context, conv Conversation, replyTo MessageID, text string) (MessageID, error)
    React(ctx context.Context, conv Conversation, msg MessageID, emoji string) error
    Menu(ctx context.Context, conv Conversation, replyTo MessageID, prompt string, opts []Choice) error
}
```

- **`degrading{Gateway}`** — décorateur qui implémente `Gateway` et dégrade selon
  `Capabilities()` : `Menu` → `Post` (liste numérotée) si `!SelectMenus` ;
  `React` → no-op si `!Reactions` ; `Reply` → `Post` si `!Replies`. La
  dégradation vit **uniquement ici**.
- Le `manager` reçoit un `Gateway` (le `degrading` enveloppant l'adaptateur) et
  n'appelle **plus jamais** le client `dctl` brut. (L'émission sortante migre de
  `internal/bridge` vers le `manager` via le port.)

### 4.2 Entrant (inbound)

La gateway produit un flux d'événements que le `manager` consomme :

```go
type Inbound struct {
    Conversation Conversation
    Author       string
    Text         string
    Attachments  []Attachment
    MessageID    MessageID
}
```

Le **session-manager** route `Inbound` → session via la table
`Conversation ↔ SessionID` (§4.4). Le câblage actuel (websocket → handler) est
ré-exprimé derrière cette frontière, sans changer le comportement.

### 4.3 Manifeste + registre — `kernel/` (contrat) + `host/` (registre)

```go
type Category string // "gateway" | "backend" | "memory" | "orchestrator"

type Manifest struct {
    Kind         string       // ex. "discord"
    Category     Category     // ex. "gateway"
    Capabilities Capabilities
    ConfigSchema json.RawMessage // schéma déclaratif (peut être minimal en 0a)
}

type Registry interface {
    Register(p Plugin)            // un plugin s'enregistre
    Gateways() []Gateway          // découverte par catégorie
}
```

- In-process : un plugin **s'enregistre** auprès du registre (tenu par `host`) au
  lieu d'être instancié en dur. `host` interroge le registre par catégorie.
- En Phase 1, l'enregistrement in-process devient l'**auto-enregistrement NATS** :
  même `Manifest`, même découverte — **aucun redesign**.

### 4.4 Identité de session

- `state` `Session` gagne `ID string` (slug/UUID **stable**, généré à la
  création), distinct de `Name` (label humain) et de l'adresse.
- L'adresse devient `Conversation{Gateway:"discord", ID:<channelID>}` (le
  `ChannelID` n'est plus une clé logique).
- Le `manager` tient `map[Conversation]SessionID` pour le routage entrant.
- **Migration `state.json`** : au chargement, toute session sans `ID` s'en voit
  générer un (idempotent). La compat lecture est préservée.

### 4.5 Adaptateur Discord — `discord/`

- Implémente `kernel.Gateway` en enveloppant le client `dctl` racine
  (`Send`/`Reply`/`React`/`SendSelectMenu`).
- `Manifest()` = `{Kind:"discord", Category:"gateway",
  Capabilities:{Reactions:true, SelectMenus:true, Replies:true}}`.
- Accueille le domaine qui quitte la racine : `dctlCommands()` (catalogue
  `/session…`), la convention `ChoiceCustomID`/`ParseChoiceCustomID`, le wiring
  `RegisterCommands`.
- S'enregistre auprès du `Registry` au démarrage.

### 4.6 Purification du package racine `dctl`

- Redevient un **client Discord pur** : REST (`Send`/`Reply`/`Read`/channels/
  threads/reactions), interactions génériques (parsers, `RegisterCommands(ctx,
  catalogue)` paramétré, autocomplete), composants génériques.
- `SendChoiceMenu` → **`SendSelectMenu`** (générique, **reste** dans `dctl`).
- `dctlCommands` / `ChoiceCustomID` / `ParseChoiceCustomID` → **partent** dans
  `discord/`.
- **Invariant** : le package racine `dctl` ne dépend **d'aucun** package de
  domaine (`core`, `bridge`, `handler`, `session`, `state`…).

## 5. Flux de données (après 0a)

```
websocket Discord
   → internal/gateway (events bruts)
   → discord/ (traduit en kernel.Inbound, résout Conversation)
   → manager/ (Conversation → SessionID, route vers la session)
   → manager/ (logique, émet via kernel.Gateway)
   → degrading{discord} (dégrade si besoin)
   → discord/ (traduit en appels dctl)
   → client dctl (REST Discord)
```

## 6. Tests

- **`degrading`** : tables de capacités → vérifie chaque dégradation (menu→liste,
  react→no-op, reply→post) et le pass-through quand la capacité est présente.
- **Identité** : génération d'`ID`, migration `state.json` (session legacy sans
  `ID`), routage `Conversation → SessionID`.
- **Adaptateur Discord** : `Manifest()` correct, traduction des 4 actions vers
  les bons appels `dctl`.
- **Pureté racine** : test de compilation/architecture garantissant que `dctl`
  n'importe aucun package domaine (ex. lint d'imports ou test dédié).
- Les tests existants (296) restent verts ; ceux qui touchaient `ChannelID`
  comme clé logique sont ré-exprimés via `Conversation`/`SessionID`.

## 7. Points d'attention assumés

- **Port méthodes vs enveloppe réseau** : `Gateway` en méthodes ne sérialise pas
  directement. Au passage process (Phase 1), un fin pont « appels ↔ messages »
  (méthode → `OutboundAction{Kind,…}` sur `msg.out`, et `Inbound` ←
  `msg.in`) fait la conversion. C'est le coût accepté du choix B ; il est **local
  à l'adaptateur**, pas dans le domaine.
- **`ConfigSchema`** peut rester minimal en 0a (placeholder typé) ; il se
  formalise quand le nombre de plugins grandit.
- **Nommage des packages** (verrouillé) :
  - `kernel/` — centre pur : entities (Session, Conversation, SessionID),
    allowlist, **ports** (Gateway/Backend/Memory/Orchestrator), Manifest,
    Registry, Capabilities. = le contrat de plugin (graine de
    `herrscher-contracts`). N'importe aucun package I/O.
  - `manager/` — orchestration (session-manager) : lifecycle, routage, mapping
    Conversation↔Session, autorisation. Consomme les ports.
  - `host/` — composition root : config, Registry/découverte, instanceid, health,
    supervisor, run loop (ex-`internal/serve`).
  - `state/` — adaptateur de persistance (SessionStore). Reste dans `herrscher-core`.
  - `discord/` (+ `forge/`) — adaptateur gateway. **Part** vers
    `herrscher-discord-gateway`.
  - **Aucun** package nommé `core`. Le *repo* s'appelle `herrscher-core` ; à
    l'intérieur, `kernel`/`manager`/`host`/`state` restent, les plugins partent.

## 8. Definition of done

- `go build ./...` + `go vet ./...` propres ; tous les tests verts.
- Le package racine `dctl` n'importe aucun package domaine (vérifié par test).
- Le `manager` n'appelle plus le client `dctl` directement, seulement
  `kernel.Gateway`.
- Discord est découvert via le `Registry` (plus de `gateway.New…` câblé en dur
  dans `host`).
- Comportement utilisateur **inchangé** (mêmes commandes, mêmes réactions, même
  rendu).

## 9. Conventions

Code propre, **pas de commentaires inutiles** ; on nettoie au passage le style
verbeux du `dctl` existant (longs doc-comments). Cf. design plateforme §7b.
