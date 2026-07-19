# Cahier de cadrage — Serveur MCP Go « Claude Reviewer » pour Codex

## 1. Objectif

Créer sur macOS un serveur MCP local écrit en Go permettant à Codex d’utiliser Claude Code comme reviewer indépendant.

Le workflow cible est :

```text
Codex implémente un changement
        │
        ▼
Codex appelle l’outil MCP review_diff
        │
        ▼
Le serveur Go lance Claude Code en lecture seule
        │
        ▼
Claude analyse le dépôt et le diff
        │
        ▼
Claude retourne une revue structurée
        │
        ▼
Codex corrige ou répond aux remarques
        │
        ▼
Codex appelle continue_review avec le même review_id
        │
        ▼
Le serveur Go reprend exactement la même session Claude
```

Le point essentiel est la conservation du contexte Claude entre plusieurs appels. Une revue commencée doit pouvoir être reprise avec ses analyses, les fichiers déjà lus, les remarques précédentes et les réponses de Codex.

---

## 2. Contraintes générales

- Langage : Go.
- Plateforme cible initiale : macOS Apple Silicon et Intel.
- Transport MCP : STDIO.
- Le programme doit fonctionner comme un binaire unique.
- Claude Code est appelé via son CLI local.
- Aucune clé Anthropic ne doit être enregistrée par le programme.
- L’authentification existante de `claude` doit être réutilisée.
- Claude agit uniquement comme reviewer.
- Claude doit pouvoir lire le dépôt, mais ne doit jamais modifier les fichiers.
- Le serveur ne doit jamais exécuter de commande Git destructive.
- Le protocole STDIO doit rester propre :
  - stdout exclusivement réservé aux messages MCP ;
  - logs et diagnostics exclusivement sur stderr.
- Le projet doit être installable et configurable par Codex directement sur le Mac.

---

## 3. Principe de persistance du contexte

Claude Code prend en charge les sessions persistantes. Le serveur doit conserver le `session_id` Claude associé à chaque revue.

### Premier appel

Lors d’un premier appel à `review_diff` :

1. générer un `review_id` ;
2. générer ou laisser Claude générer un `session_id` ;
3. lancer Claude Code en mode non interactif ;
4. récupérer le `session_id` dans la sortie structurée ;
5. enregistrer l’association :
   - `review_id` ;
   - `claude_session_id` ;
   - chemin canonique du dépôt ;
   - date de création ;
   - date de dernière utilisation ;
   - objectif de la revue ;
   - base Git utilisée ;
   - état de la revue.

### Appels suivants

Lors d’un appel à `continue_review` :

1. retrouver l’enregistrement grâce au `review_id` ;
2. vérifier que le dépôt demandé correspond au dépôt de la session ;
3. relancer Claude avec :

```bash
claude -p --resume "<claude_session_id>" ...
```

4. envoyer uniquement le nouveau message à Claude ;
5. ne pas reconstruire artificiellement tout l’historique dans le prompt ;
6. mettre à jour la date de dernière utilisation.

### Persistance sur disque

La persistance doit survivre :

- à plusieurs appels MCP ;
- à un redémarrage du serveur MCP ;
- à un redémarrage de Codex ;
- idéalement à un redémarrage du Mac.

Stockage recommandé pour la V1 :

```text
~/Library/Application Support/claude-reviewer/sessions.json
```

Un stockage JSON atomique suffit pour la V1.

Écriture atomique obligatoire :

1. écrire dans un fichier temporaire du même dossier ;
2. appeler `Sync()` ;
3. renommer le fichier temporaire vers `sessions.json`.

Prévoir une interface Go de stockage afin de pouvoir remplacer ultérieurement JSON par SQLite sans modifier le reste du code.

```go
type SessionStore interface {
    Create(ctx context.Context, session ReviewSession) error
    Get(ctx context.Context, reviewID string) (ReviewSession, error)
    Update(ctx context.Context, session ReviewSession) error
    Delete(ctx context.Context, reviewID string) error
    List(ctx context.Context) ([]ReviewSession, error)
}
```

Ne jamais utiliser simplement `claude --continue`, car cette option reprend la dernière session du répertoire et peut donc reprendre la mauvaise conversation. Toujours utiliser un identifiant explicite avec `--resume`.

---

## 4. Outils MCP à exposer

### 4.1 `review_diff`

Démarre une nouvelle revue et crée une nouvelle session Claude.

Entrée :

```json
{
  "repository_path": "/chemin/absolu/du/repo",
  "goal": "Description du changement réalisé",
  "base_ref": "HEAD",
  "review_focus": [
    "correctness",
    "regressions",
    "architecture",
    "performance",
    "security",
    "tests"
  ],
  "additional_context": "Contexte facultatif",
  "test_results": "Résultats facultatifs des tests déjà exécutés",
  "model": "opus",
  "max_turns": 12
}
```

Règles :

- `repository_path` est obligatoire.
- Le chemin doit exister.
- Le chemin doit être un dépôt Git.
- Convertir le chemin en chemin absolu canonique.
- `goal` est obligatoire.
- `base_ref` vaut `HEAD` par défaut.
- Le diff doit être calculé par le serveur, pas fourni aveuglément par Codex.
- Le serveur peut utiliser :
  - `git diff <base_ref>` pour les modifications non commitées ;
  - les fichiers non suivis doivent être signalés séparément ;
  - ne jamais envoyer automatiquement de fichiers manifestement secrets.
- La réponse doit contenir le nouveau `review_id`.

### 4.2 `continue_review`

Reprend la même conversation Claude.

Entrée :

```json
{
  "review_id": "uuid-de-la-revue",
  "message": "J’ai corrigé les points F001 et F003. Vérifie le nouveau diff.",
  "refresh_diff": true,
  "test_results": "Nouveaux résultats facultatifs"
}
```

Règles :

- reprendre la session avec son `claude_session_id` ;
- utiliser le même répertoire de travail ;
- si `refresh_diff` vaut `true`, recalculer le diff courant et l’ajouter au nouveau message ;
- rappeler à Claude qu’il s’agit de la suite de la revue, sans répéter tout le prompt initial ;
- retourner une nouvelle réponse structurée ;
- conserver le même `review_id`.

### 4.3 `get_review`

Retourne les métadonnées locales d’une revue sans appeler Claude.

Entrée :

```json
{
  "review_id": "uuid-de-la-revue"
}
```

Sortie :

```json
{
  "review_id": "...",
  "claude_session_id": "...",
  "repository_path": "...",
  "goal": "...",
  "created_at": "...",
  "updated_at": "...",
  "status": "open"
}
```

Ne pas retourner de secret ni le contenu intégral de la conversation.

### 4.4 `list_reviews`

Liste les revues persistées, triées de la plus récente à la plus ancienne.

Entrée facultative :

```json
{
  "repository_path": "/chemin/facultatif",
  "status": "open"
}
```

### 4.5 `close_review`

Marque une revue comme fermée.

Entrée :

```json
{
  "review_id": "uuid-de-la-revue",
  "delete_claude_session": false
}
```

Pour la V1, il n’est pas nécessaire de supprimer les données natives de Claude Code. Il suffit de fermer ou supprimer l’association locale selon l’option choisie.

---

## 5. Exécution de Claude Code

### Commande initiale recommandée

Utiliser une sortie en flux JSON afin de récupérer de manière robuste le message d’initialisation et le `session_id`.

Forme générale :

```bash
claude \
  -p \
  --output-format stream-json \
  --verbose \
  --permission-mode dontAsk \
  --tools "Read,Glob,Grep" \
  --disallowedTools "Edit" "Write" "NotebookEdit" "Bash" "WebSearch" "WebFetch" "mcp__*" \
  --max-turns 12 \
  --model opus \
  "<prompt>"
```

Le processus doit être lancé avec :

```go
cmd.Dir = repositoryPath
```

### Reprise de session

```bash
claude \
  -p \
  --resume "<claude_session_id>" \
  --output-format stream-json \
  --verbose \
  --permission-mode dontAsk \
  --tools "Read,Glob,Grep" \
  --disallowedTools "Edit" "Write" "NotebookEdit" "Bash" "WebSearch" "WebFetch" "mcp__*" \
  --max-turns 12 \
  "<message-de-suivi>"
```

### Sécurité lecture seule

Claude ne doit disposer que de :

- `Read` ;
- `Glob` ;
- `Grep`.

Tout outil d’écriture ou d’exécution doit être interdit.

Le serveur Go calcule lui-même le diff Git avant de lancer Claude. Cela évite de donner à Claude l’outil Bash.

Interdire au minimum :

- `Edit` ;
- `Write` ;
- `NotebookEdit` ;
- `Bash` ;
- `WebSearch` ;
- `WebFetch` ;
- tous les outils MCP externes.

Ne pas utiliser `--dangerously-skip-permissions`.

### Timeout et interruption

- Timeout par défaut d’un appel Claude : 10 minutes.
- Timeout configurable dans le fichier de configuration.
- Utiliser `exec.CommandContext`.
- En cas d’annulation MCP, interrompre le processus enfant.
- Sur macOS, s’assurer que les processus descendants ne restent pas orphelins.
- Capturer stderr séparément pour les diagnostics.
- Limiter la taille des sorties conservées en mémoire.

---

## 6. Récupération du `session_id`

Le parseur de `stream-json` doit lire stdout ligne par ligne.

Lorsqu’un événement système d’initialisation est reçu, extraire le `session_id`.

Le code ne doit pas dépendre de l’ordre exact de tous les événements, uniquement de champs structurants tels que :

```json
{
  "type": "system",
  "subtype": "init",
  "session_id": "..."
}
```

La forme exacte pouvant évoluer, le parseur doit :

- ignorer les champs inconnus ;
- accepter que le `session_id` soit présent directement ou dans une structure enveloppante ;
- conserver les lignes non reconnues dans les logs de debug ;
- échouer clairement si aucun `session_id` n’est obtenu lors d’une nouvelle revue ;
- accepter qu’une reprise conserve le même identifiant.

Ajouter des tests unitaires avec plusieurs exemples de flux JSON.

---

## 7. Format de réponse demandé à Claude

Utiliser `--json-schema` lorsque la version locale de Claude Code le prend en charge.

Schéma logique :

```json
{
  "type": "object",
  "required": ["verdict", "summary", "findings", "missing_tests"],
  "properties": {
    "verdict": {
      "type": "string",
      "enum": ["approve", "changes_requested", "needs_context"]
    },
    "summary": {
      "type": "string"
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": [
          "id",
          "severity",
          "category",
          "file",
          "problem",
          "impact",
          "recommendation",
          "confidence"
        ],
        "properties": {
          "id": {
            "type": "string"
          },
          "severity": {
            "type": "string",
            "enum": ["critical", "high", "medium", "low"]
          },
          "category": {
            "type": "string",
            "enum": [
              "correctness",
              "regression",
              "architecture",
              "performance",
              "security",
              "concurrency",
              "maintainability",
              "test"
            ]
          },
          "file": {
            "type": "string"
          },
          "line": {
            "type": ["integer", "null"]
          },
          "problem": {
            "type": "string"
          },
          "impact": {
            "type": "string"
          },
          "recommendation": {
            "type": "string"
          },
          "confidence": {
            "type": "number",
            "minimum": 0,
            "maximum": 1
          }
        }
      }
    },
    "missing_tests": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "questions": {
      "type": "array",
      "items": {
        "type": "string"
      }
    }
  }
}
```

Chaque finding doit avoir un identifiant stable dans la conversation :

```text
F001
F002
F003
```

Lors d’une reprise, Claude doit explicitement indiquer pour les anciens findings :

- `resolved` ;
- `still_open` ;
- `invalidated` ;
- ou `partially_resolved`.

Le schéma peut donc être étendu pour `continue_review` avec :

```json
{
  "previous_findings": [
    {
      "id": "F001",
      "status": "resolved",
      "comment": "..."
    }
  ]
}
```

---

## 8. Prompt système du reviewer

Créer un fichier embarqué, par exemple :

```text
internal/reviewer/prompts/reviewer.md
```

Contenu attendu :

```text
Tu es un reviewer logiciel senior indépendant.

Tu n’es pas l’auteur du changement. Ton rôle est de rechercher activement les
défauts réels, les régressions, les hypothèses fragiles et les tests manquants.

Tu travailles en lecture seule. Tu ne dois jamais modifier de fichier.

Règles :
- Examine le diff, puis lis les fichiers environnants nécessaires.
- Vérifie les appels entrants et sortants des fonctions modifiées.
- Ne signale pas de préférences stylistiques sans impact concret.
- Chaque finding doit décrire un scénario reproductible ou un risque précis.
- Cite le fichier et, lorsque possible, la ligne.
- Ne prétends pas avoir exécuté des tests.
- Distingue faits, hypothèses et incertitudes.
- Ne recommande pas une refonte générale lorsqu’une correction locale suffit.
- Tiens compte de l’objectif fonctionnel fourni.
- Pour une reprise de revue, conserve les identifiants des anciens findings.
- Vérifie les corrections avant de marquer un finding comme résolu.
- Une approbation signifie qu’aucun problème significatif n’a été identifié,
  pas que le code est garanti sans défaut.
```

Le premier prompt utilisateur doit inclure :

- l’objectif ;
- la base Git ;
- le diff ;
- les fichiers non suivis ;
- les résultats des tests fournis ;
- le contexte additionnel ;
- les axes de revue demandés ;
- l’instruction de lire le code environnant en lecture seule.

---

## 9. Gestion du diff

Créer un composant Git dédié.

Interface suggérée :

```go
type GitService interface {
    ValidateRepository(ctx context.Context, path string) error
    Root(ctx context.Context, path string) (string, error)
    Diff(ctx context.Context, path, baseRef string) (string, error)
    UntrackedFiles(ctx context.Context, path string) ([]string, error)
    CurrentBranch(ctx context.Context, path string) (string, error)
    HeadSHA(ctx context.Context, path string) (string, error)
}
```

Commandes autorisées dans le processus Go :

```bash
git rev-parse --show-toplevel
git rev-parse --verify <base_ref>
git rev-parse HEAD
git branch --show-current
git diff --no-ext-diff --unified=80 <base_ref> --
git status --porcelain=v1
```

Utiliser `exec.CommandContext` avec des arguments séparés. Ne jamais construire une commande shell concaténée.

### Limite de taille

Le diff peut être énorme.

Prévoir :

- limite configurable, par défaut 2 Mio ;
- détection des fichiers binaires ;
- message d’erreur explicite si le diff dépasse la limite ;
- possibilité future de découper la revue par fichiers ;
- ne jamais tronquer silencieusement.

Pour la V1, si la limite est dépassée, retourner une erreur expliquant à Codex qu’il doit réduire la portée du changement ou choisir une future option de filtrage.

---

## 10. Protection contre les secrets

Avant d’envoyer le diff à Claude, détecter au minimum :

- fichiers `.env` ;
- clés privées ;
- noms de fichiers contenant `secret`, `credentials`, `token` ;
- blocs ressemblant à des clés PEM ;
- chaînes manifestement assimilables à des tokens.

Comportement :

- ne pas enregistrer les secrets dans les logs ;
- remplacer leur valeur par `[REDACTED]` lorsque cela est raisonnable ;
- refuser la revue si une clé privée complète est détectée ;
- indiquer à Codex quels fichiers ont été exclus ou masqués.

Ne pas prétendre que cette détection est infaillible.

---

## 11. Configuration

Créer un fichier facultatif :

```text
~/Library/Application Support/claude-reviewer/config.json
```

Exemple :

```json
{
  "claude_binary": "/opt/homebrew/bin/claude",
  "default_model": "opus",
  "default_max_turns": 12,
  "timeout_seconds": 600,
  "max_diff_bytes": 2097152,
  "log_level": "info",
  "session_retention_days": 30
}
```

Résolution du binaire Claude :

1. configuration explicite ;
2. `exec.LookPath("claude")` ;
3. chemins Homebrew courants :
   - `/opt/homebrew/bin/claude` ;
   - `/usr/local/bin/claude`.

Ne jamais coder uniquement un chemin Apple Silicon.

Ajouter une commande :

```bash
claude-reviewer doctor
```

Elle doit vérifier :

- présence du binaire Claude ;
- version de Claude Code ;
- authentification avec `claude auth status` ;
- présence de Git ;
- accès en écriture au dossier de données ;
- validité du stockage des sessions ;
- compatibilité des flags CLI nécessaires.

Le mode MCP reste la commande par défaut :

```bash
claude-reviewer
```

ou explicitement :

```bash
claude-reviewer serve
```

---

## 12. Structure de projet souhaitée

```text
claude-reviewer/
├── cmd/
│   └── claude-reviewer/
│       └── main.go
├── internal/
│   ├── claude/
│   │   ├── client.go
│   │   ├── parser.go
│   │   └── client_test.go
│   ├── config/
│   │   ├── config.go
│   │   └── paths_darwin.go
│   ├── git/
│   │   ├── service.go
│   │   └── service_test.go
│   ├── mcp/
│   │   ├── server.go
│   │   └── tools.go
│   ├── reviewer/
│   │   ├── service.go
│   │   ├── prompt.go
│   │   ├── schema.go
│   │   └── prompts/
│   │       └── reviewer.md
│   ├── session/
│   │   ├── model.go
│   │   ├── store.go
│   │   ├── json_store.go
│   │   └── json_store_test.go
│   └── security/
│       ├── redact.go
│       └── redact_test.go
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── AGENTS.md
└── LICENSE
```

Choisir une bibliothèque MCP Go maintenue et raisonnablement légère. Avant de l’ajouter, vérifier sa documentation actuelle et privilégier un SDK officiel ou clairement maintenu. Isoler la bibliothèque derrière le package `internal/mcp` pour limiter le couplage.

---

## 13. Modèle de données

```go
type ReviewSession struct {
    ReviewID        string       `json:"review_id"`
    ClaudeSessionID string       `json:"claude_session_id"`
    RepositoryPath  string       `json:"repository_path"`
    Goal            string       `json:"goal"`
    BaseRef         string       `json:"base_ref"`
    HeadSHAAtStart  string       `json:"head_sha_at_start"`
    Model           string       `json:"model"`
    Status          ReviewStatus `json:"status"`
    CreatedAt       time.Time    `json:"created_at"`
    UpdatedAt       time.Time    `json:"updated_at"`
}

type ReviewStatus string

const (
    ReviewStatusOpen   ReviewStatus = "open"
    ReviewStatusClosed ReviewStatus = "closed"
)
```

Les chemins doivent être normalisés avec résolution des liens symboliques lorsque possible.

Le serveur doit empêcher l’utilisation d’un `review_id` dans un autre dépôt.

---

## 14. Concurrence

Plusieurs appels MCP peuvent arriver simultanément.

Règles :

- autoriser des revues différentes en parallèle ;
- interdire deux appels simultanés sur le même `review_id` ;
- utiliser un verrou par session ;
- protéger le stockage JSON ;
- écrire les données atomiquement ;
- retourner une erreur claire `review_busy` si une session est déjà utilisée ;
- ne pas bloquer toutes les revues à cause d’une seule session longue.

---

## 15. Erreurs MCP structurées

Définir des erreurs exploitables :

- `invalid_repository`
- `invalid_base_ref`
- `review_not_found`
- `review_closed`
- `review_busy`
- `repository_mismatch`
- `claude_not_found`
- `claude_not_authenticated`
- `claude_timeout`
- `claude_failed`
- `claude_session_id_missing`
- `invalid_claude_output`
- `diff_too_large`
- `sensitive_content_detected`
- `storage_error`

Chaque erreur doit comporter :

```json
{
  "code": "review_not_found",
  "message": "Aucune revue ne correspond à cet identifiant.",
  "details": {}
}
```

Ne jamais retourner une stack trace brute à Codex.

---

## 16. Logs

Logs JSON ou `slog`.

Inclure :

- timestamp ;
- niveau ;
- outil MCP ;
- `review_id` ;
- durée ;
- code de sortie Claude ;
- taille du diff ;
- nombre de findings.

Ne pas logger :

- diff complet ;
- prompts complets ;
- secrets ;
- réponse Claude complète par défaut.

Prévoir un mode debug explicite.

Tous les logs doivent aller sur stderr.

---

## 17. Installation macOS

Le README doit fournir les commandes suivantes.

### Compilation

```bash
go build -o ./bin/claude-reviewer ./cmd/claude-reviewer
```

### Installation utilisateur

```bash
mkdir -p "$HOME/.local/bin"
cp ./bin/claude-reviewer "$HOME/.local/bin/claude-reviewer"
chmod +x "$HOME/.local/bin/claude-reviewer"
```

Vérifier que `~/.local/bin` est dans le `PATH`.

### Diagnostic

```bash
claude-reviewer doctor
```

### Ajout dans Codex

Commande préférée :

```bash
codex mcp add claude-reviewer -- "$HOME/.local/bin/claude-reviewer" serve
```

Puis :

```bash
codex mcp list
```

Configuration TOML équivalente :

```toml
[mcp_servers.claude-reviewer]
command = "/Users/UTILISATEUR/.local/bin/claude-reviewer"
args = ["serve"]
```

Ne pas écrire littéralement `$HOME` dans le champ `command` TOML, car l’expansion shell n’est pas garantie. Le script d’installation doit injecter le chemin absolu réel.

---

## 18. Instructions Codex à installer dans `AGENTS.md`

Le projet doit fournir ce bloc prêt à copier :

```md
## Revue croisée avec Claude

Pour tout changement non trivial :

1. Implémente le changement.
2. Exécute les tests, le lint et le type-checking pertinents.
3. Appelle `claude-reviewer.review_diff`.
4. Fournis un objectif précis et les résultats de tests.
5. Analyse chaque finding au lieu de l’accepter aveuglément.
6. Corrige les findings confirmés de sévérité critical, high ou medium.
7. Pour les remarques incorrectes, prépare une réponse technique factuelle.
8. Appelle `claude-reviewer.continue_review` avec le même `review_id`.
9. Demande à Claude de vérifier les corrections et de réévaluer les anciens findings.
10. Arrête après deux cycles complets sauf problème critique restant.
11. Ne considère pas une approbation Claude comme un remplacement des tests.
12. Claude est reviewer en lecture seule ; Codex reste le seul agent qui modifie le dépôt.
```

---

## 19. Tests obligatoires

### Tests unitaires

- parsing du flux JSON Claude ;
- extraction du `session_id` ;
- réponse structurée ;
- stockage et reprise d’une session ;
- écriture atomique ;
- verrouillage par `review_id` ;
- détection d’un dépôt différent ;
- validation de `base_ref` ;
- redaction de secrets ;
- limite de taille du diff ;
- erreurs de timeout ;
- erreurs de processus Claude.

### Tests d’intégration

Utiliser un faux exécutable `claude` configurable dans les tests.

Scénario minimum :

1. `review_diff` lance le faux Claude ;
2. le faux Claude retourne un événement init avec `session_id = A` ;
3. le serveur persiste `A` ;
4. le serveur retourne un `review_id = R` ;
5. `continue_review(R)` relance le faux Claude avec `--resume A` ;
6. vérifier que le nouveau prompt ne contient que le suivi nécessaire ;
7. redémarrer une nouvelle instance du store ;
8. vérifier que `continue_review(R)` fonctionne toujours.

### Test manuel réel

Sur un petit dépôt Git :

1. créer une modification volontairement incorrecte ;
2. appeler `review_diff` depuis Codex ;
3. noter le `review_id` ;
4. corriger partiellement ;
5. appeler `continue_review` ;
6. vérifier que Claude se souvient du finding initial ;
7. arrêter puis relancer Codex ;
8. appeler encore `continue_review` avec le même identifiant ;
9. vérifier que le contexte est conservé.

---

## 20. Critères d’acceptation

Le projet est terminé lorsque :

- le binaire démarre comme serveur MCP STDIO ;
- Codex voit les cinq outils ;
- une revue initiale produit un `review_id` ;
- le `claude_session_id` est persisté ;
- un suivi reprend la même session avec `--resume` ;
- le contexte survit au redémarrage du serveur ;
- Claude ne peut ni écrire ni exécuter Bash ;
- le diff est calculé par le serveur ;
- les réponses sont structurées et validées ;
- les erreurs sont propres et exploitables ;
- stdout ne contient jamais de logs ;
- `go test ./...` passe ;
- `go vet ./...` passe ;
- le README permet une installation complète sur macOS ;
- `claude-reviewer doctor` valide l’environnement local ;
- Codex peut installer le serveur avec une commande documentée.

---

## 21. Hors périmètre V1

Ne pas implémenter dans la première version :

- serveur MCP HTTP distant ;
- interface graphique ;
- GitHub App ;
- création automatique de commentaires de PR ;
- modification de code par Claude ;
- orchestration de plusieurs modèles Claude ;
- base de données réseau ;
- synchronisation entre plusieurs Macs ;
- gestion centralisée d’équipe ;
- envoi automatique de télémétrie ;
- revue de diff de plus de 2 Mio par découpage automatique.

Préparer les interfaces pour permettre ces évolutions, mais ne pas sur-concevoir la V1.

---

## 22. Ordre d’implémentation demandé à Codex

1. Initialiser le module Go.
2. Choisir et intégrer la bibliothèque MCP Go.
3. Créer le serveur STDIO et un outil temporaire `ping`.
4. Implémenter les chemins macOS et la configuration.
5. Implémenter `SessionStore` JSON avec tests.
6. Implémenter le service Git avec tests.
7. Implémenter le client Claude et le parseur `stream-json`.
8. Implémenter le prompt et le schéma de réponse.
9. Implémenter `review_diff`.
10. Implémenter `continue_review`.
11. Implémenter `get_review`, `list_reviews`, `close_review`.
12. Ajouter les protections de sécurité et les limites.
13. Ajouter les verrous par session.
14. Ajouter `doctor`.
15. Écrire les tests d’intégration avec faux Claude.
16. Écrire le README et le bloc `AGENTS.md`.
17. Compiler et effectuer un test réel avec Claude Code local.
18. Ajouter une commande d’installation Codex.
19. Exécuter :
    - `gofmt` ;
    - `go test ./...` ;
    - `go vet ./...`.
20. Fournir un résumé final :
    - fichiers créés ;
    - architecture ;
    - commandes d’installation ;
    - limites restantes ;
    - preuve qu’une session est bien reprise.

---

## 23. Décisions importantes à ne pas modifier sans justification

- Go est obligatoire.
- MCP STDIO est obligatoire pour la V1.
- Le contexte doit être conservé via le vrai `session_id` Claude et `--resume`.
- L’association `review_id` → `claude_session_id` doit être persistée sur disque.
- Claude est strictement en lecture seule.
- Le serveur calcule le diff.
- stdout est réservé au protocole MCP.
- `claude --continue` ne doit pas être utilisé.
- Un `review_id` ne peut pas migrer silencieusement vers un autre dépôt.
- Les sessions concurrentes doivent être verrouillées individuellement.
- Les retours Claude doivent être structurés et validés.
