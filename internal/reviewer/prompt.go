package reviewer

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed prompts/reviewer.md
var SystemPrompt string

type InitialPromptInput struct {
	Goal, BaseRef, Diff, AdditionalContext, TestResults string
	ReviewFocus, UntrackedFiles, ExcludedFiles          []string
	RedactionCount                                      int
}

func InitialPrompt(in InitialPromptInput) string {
	return fmt.Sprintf(`Effectue une revue indépendante du changement suivant.

OBJECTIF
%s

BASE GIT
%s

AXES DE REVUE
%s

DIFF CALCULÉ PAR LE SERVEUR
%s

FICHIERS NON SUIVIS (contenu non inclus)
%s

CONTENU SENSIBLE
Fichiers exclus: %s
Valeurs masquées: %d

RÉSULTATS DE TESTS FOURNIS PAR L’AUTEUR
%s

CONTEXTE ADDITIONNEL
%s

Lis en lecture seule le code environnant nécessaire. Retourne uniquement la réponse structurée demandée.`,
		none(in.Goal), none(in.BaseRef), list(in.ReviewFocus), none(in.Diff), list(in.UntrackedFiles), list(in.ExcludedFiles), in.RedactionCount, none(in.TestResults), none(in.AdditionalContext))
}

func ContinuePrompt(message, diff, testResults string, untracked, excluded []string, redactions int) string {
	var b strings.Builder
	b.WriteString("Suite de la même revue. Conserve le contexte et les identifiants des findings précédents. Vérifie les corrections avant de mettre à jour previous_findings.\n\nNOUVEAU MESSAGE\n")
	b.WriteString(none(message))
	if diff != "" || len(untracked) > 0 || len(excluded) > 0 {
		fmt.Fprintf(&b, "\n\nDIFF COURANT RECALCULÉ\n%s\n\nFICHIERS NON SUIVIS\n%s\n\nCONTENU SENSIBLE\nFichiers exclus: %s\nValeurs masquées: %d", none(diff), list(untracked), list(excluded), redactions)
	}
	if testResults != "" {
		b.WriteString("\n\nNOUVEAUX RÉSULTATS DE TESTS FOURNIS\n" + testResults)
	}
	b.WriteString("\n\nRetourne uniquement la réponse structurée demandée.")
	return b.String()
}

func none(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(aucun)"
	}
	return s
}
func list(v []string) string {
	if len(v) == 0 {
		return "(aucun)"
	}
	return strings.Join(v, ", ")
}
