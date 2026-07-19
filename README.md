# Claude Reviewer MCP

Serveur MCP local en Go qui permet à Codex de confier la revue d’un diff Git à
Claude Code en lecture seule. Chaque `review_id` est associé durablement au
`session_id` explicite de Claude. Les suivis utilisent exclusivement
`claude -p --resume <session_id>` : l’option ambiguë `--continue` n’est jamais
utilisée.

## Garanties principales

- transport MCP STDIO, avec stdout réservé au protocole et logs JSON sur stderr ;
- cinq outils : `review_diff`, `continue_review`, `get_review`, `list_reviews`,
  `close_review` ;
- annotations MCP explicites : lectures marquées read-only, fermeture marquée
  destructive, afin que les politiques d’approbation Codex soient correctes ;
- diff calculé localement par arguments Git séparés, sans shell ni commande
  destructive ;
- Claude limité à `Read,Glob,Grep`, avec écriture, Bash, Web et outils MCP
  interdits ;
- prompt et diff transmis à Claude par stdin, jamais dans la ligne de commande
  visible par `ps` et sans dépendre de la limite macOS `ARG_MAX` ;
- réponses imposées par JSON Schema puis validées côté Go ;
- sessions dans `~/Library/Application Support/claude-reviewer/sessions.json`,
  écrites atomiquement avec `Sync`, renommage et permissions `0600` ;
- verrou non bloquant par revue (`review_busy`) ; les autres revues restent
  parallélisables ;
- limite de diff de 2 Mio par défaut, jamais tronqué silencieusement ;
- exclusion des noms de fichiers sensibles, redaction de tokens courants et
  refus d’une clé privée complète.

La détection de secrets est une défense complémentaire et n’est pas infaillible.
Les fichiers non suivis sont signalés par leur nom mais leur contenu n’est pas
envoyé automatiquement.

## Prérequis

- macOS (Apple Silicon ou Intel) ;
- Go 1.25 ou plus récent ;
- Git ;
- Claude Code installé et authentifié (`claude auth login`) ;
- Codex CLI pour l’enregistrement automatique du serveur.

## Compiler et tester

```bash
go build -o ./bin/claude-reviewer ./cmd/claude-reviewer
go test ./...
go vet ./...
```

Ou :

```bash
make check
```

## Installation utilisateur sur macOS

```bash
mkdir -p "$HOME/.local/bin"
cp ./bin/claude-reviewer "$HOME/.local/bin/claude-reviewer"
chmod +x "$HOME/.local/bin/claude-reviewer"
```

Ajoutez `~/.local/bin` au `PATH` si nécessaire, puis diagnostiquez
l’environnement :

```bash
claude-reviewer doctor
```

Le rapport JSON vérifie le binaire et la version de Claude Code,
l’authentification, Git, l’écriture dans le dossier de données, le stockage des
sessions et les flags CLI requis.

## Installation MCP dans Codex

La commande recommandée injecte le chemin absolu réel :

```bash
codex mcp add claude-reviewer -- "$HOME/.local/bin/claude-reviewer" serve
codex mcp list
```

Configuration TOML équivalente (remplacez le nom d’utilisateur) :

```toml
[mcp_servers.claude-reviewer]
command = "/Users/UTILISATEUR/.local/bin/claude-reviewer"
args = ["serve"]
```

N’écrivez pas littéralement `$HOME` dans `command` : aucune expansion shell
n’est garantie dans ce champ.

Le serveur démarre sans sous-commande ou explicitement avec :

```bash
claude-reviewer
claude-reviewer serve
```

## Utilisation

`review_diff` attend au minimum `repository_path` et `goal`. `base_ref` vaut
`HEAD`, le modèle principal vaut `fable`, le fallback vaut `opus`, l’effort vaut
`max` et `max_turns` vaut 12. Le résultat contient un
nouveau `review_id` et le `claude_session_id` persisté.

L’effort `max` privilégie délibérément la qualité de revue au détriment du coût
et de la latence. Un appel peut choisir un effort inférieur parmi `low`,
`medium`, `high` et `xhigh`.

`continue_review` attend le même `review_id` et un nouveau `message`. Avec
`refresh_diff: true`, le serveur recalcule le diff et l’ajoute au seul message
de suivi. Il recharge l’association depuis le disque et invoque exactement :

```text
claude -p --resume <claude_session_id> ... <nouveau-message>
```

Le contexte conversationnel appartient donc à la session native Claude et
survit aux redémarrages du serveur, de Codex et du Mac.

`get_review` ne contacte pas Claude. `list_reviews` accepte les filtres
`repository_path` et `status`. `close_review` ferme l’association ; avec
`delete_claude_session: true`, la V1 supprime seulement l’association locale,
pas les données natives de Claude Code.

## Configuration facultative

Créez `~/Library/Application Support/claude-reviewer/config.json` :

```json
{
  "claude_binary": "/opt/homebrew/bin/claude",
  "default_model": "fable",
  "default_fallback_model": "opus",
  "default_effort": "max",
  "default_max_turns": 12,
  "timeout_seconds": 600,
  "max_diff_bytes": 2097152,
  "max_output_bytes": 8388608,
  "log_level": "info",
  "session_retention_days": 30
}
```

Sans chemin explicite, la résolution essaie le `PATH`, puis les chemins
Homebrew Apple Silicon et Intel. `session_retention_days` est réservé au futur
nettoyage explicite ; aucune session n’est supprimée automatiquement en V1.

## Erreurs

Les erreurs d’outil sont du JSON exploitable (`code`, `message`, `details`) et
n’exposent ni stack trace, ni prompt, ni diff. Les codes incluent notamment
`invalid_repository`, `invalid_base_ref`, `review_not_found`, `review_closed`,
`review_busy`, `repository_mismatch`, `claude_not_found`,
`claude_not_authenticated`, `claude_timeout`, `claude_failed`,
`claude_session_id_missing`, `invalid_claude_output`, `diff_too_large`,
`claude_output_too_large`, `sensitive_content_detected` et `storage_error`.

## Limites V1

Pas de serveur HTTP, interface graphique, GitHub App, commentaire de PR,
modification de code par Claude, base réseau, synchronisation multi-Mac,
télémétrie ou découpage automatique des diffs supérieurs à 2 Mio. Fermer une
revue ne supprime pas sa conversation dans le stockage natif de Claude Code.
