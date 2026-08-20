//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package metrics

import (
	"fmt"
	"strings"
)

// AnswerMetrics holds deterministic answer-quality metrics.
type AnswerMetrics struct {
	F1       float64 `json:"f1"`
	BLEU     float64 `json:"bleu"`
	ROUGE1   float64 `json:"rouge_1"`
	ROUGE2   float64 `json:"rouge_2"`
	ROUGEL   float64 `json:"rouge_l"`
	Accuracy float64 `json:"accuracy"`
}

const longMemEvalJudgeOutputInstruction = "\n\nOutput constraint: Answer exactly one word: yes or no. If the case is ambiguous, choose the more likely label. Do not output uncertainty or explanation. Your entire response must be within 10 tokens."

const longMemEvalJudgeContradictionInstruction = " If the model response mentions the correct answer but clearly says it does not apply to the asked question, date, time, or context, answer no."

// CalculateAnswerMetrics computes deterministic text metrics.
func CalculateAnswerMetrics(prediction, groundTruth string) AnswerMetrics {
	return AnswerMetrics{
		F1:     CalculateF1(prediction, groundTruth),
		BLEU:   CalculateBLEU(prediction, groundTruth),
		ROUGE1: CalculateROUGE1(prediction, groundTruth),
		ROUGE2: CalculateROUGE2(prediction, groundTruth),
		ROUGEL: CalculateROUGEL(prediction, groundTruth),
	}
}

// Add merges another AnswerMetrics into the receiver.
func (m *AnswerMetrics) Add(other AnswerMetrics) {
	m.F1 += other.F1
	m.BLEU += other.BLEU
	m.ROUGE1 += other.ROUGE1
	m.ROUGE2 += other.ROUGE2
	m.ROUGEL += other.ROUGEL
	m.Accuracy += other.Accuracy
}

// Divide divides all metrics by n.
func (m *AnswerMetrics) Divide(n float64) {
	if n == 0 {
		return
	}
	m.F1 /= n
	m.BLEU /= n
	m.ROUGE1 /= n
	m.ROUGE2 /= n
	m.ROUGEL /= n
	m.Accuracy /= n
}

// LongMemEvalJudgePrompt returns the official LongMemEval QA judge prompt.
func LongMemEvalJudgePrompt(
	task, question, answer, response string,
	abstention bool,
) (string, error) {
	if abstention {
		template := "I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the model correctly identifies the question as unanswerable. The model could say that the information is incomplete, or some other information is given but the asked information is not.\n\nQuestion: %s\n\nExplanation: %s\n\nModel Response: %s\n\nDoes the model correctly identify the question as unanswerable?" + longMemEvalJudgeOutputInstruction
		return fmt.Sprintf(template, question, answer, response), nil
	}
	switch task {
	case "single-session-user", "single-session-assistant", "multi-session":
		template := "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no." + longMemEvalJudgeContradictionInstruction + " \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct?" + longMemEvalJudgeOutputInstruction
		return fmt.Sprintf(template, question, answer, response), nil
	case "temporal-reasoning":
		template := "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no. In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), the model's response is still correct." + longMemEvalJudgeContradictionInstruction + " \n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct?" + longMemEvalJudgeOutputInstruction
		return fmt.Sprintf(template, question, answer, response), nil
	case "knowledge-update":
		template := "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no. If the response contains some previous information along with an updated answer, the response should be considered as correct as long as the updated answer is the required answer." + longMemEvalJudgeContradictionInstruction + "\n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct?" + longMemEvalJudgeOutputInstruction
		return fmt.Sprintf(template, question, answer, response), nil
	case "single-session-preference":
		template := "I will give you a question, a rubric for desired personalized response, and a response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls and utilizes the user's personal information correctly.\n\nQuestion: %s\n\nRubric: %s\n\nModel Response: %s\n\nIs the model response correct?" + longMemEvalJudgeOutputInstruction
		return fmt.Sprintf(template, question, answer, response), nil
	default:
		return "", fmt.Errorf("unsupported LongMemEval question type %q", task)
	}
}

// ParseLongMemEvalJudgeLabel parses the yes/no label from a judge response.
func ParseLongMemEvalJudgeLabel(response string) (bool, error) {
	normalized := strings.TrimSpace(strings.ToLower(response))
	normalized = strings.Trim(normalized, ".! \n\t\r")
	switch normalized {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("judge response is not an exact yes/no: %q", response)
	}
}
