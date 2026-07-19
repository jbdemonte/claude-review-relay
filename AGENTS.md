# Instructions du projet

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
