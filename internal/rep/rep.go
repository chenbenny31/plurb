// Package rep holds the reputation rules: how much a vote weighs, how much
// reputation it moves, and the caps that blunt vote rings. Pure functions,
// so the policy can be read and tested in one place; the store applies it
// transactionally.
package rep

import (
	"math"
	"time"
)

const (
	// Accounts younger than this vote with weight 0. The vote is recorded,
	// but since weights are snapshotted it never counts, so freshly made
	// sockpuppets can't pump scores.
	MinAccountAge = 24 * time.Hour
	// MaxWeight bounds any single voter's influence.
	MaxWeight = 5.0
	// DownvoteMinRep gates downvoting behind some earned trust.
	DownvoteMinRep = 15

	ThreadUpvote   = 5
	CommentUpvote  = 2
	DownvoteReward = -1

	// PerVoterDailyCap limits the net reputation one voter can give one
	// author per 24h, so a pair of friends can't farm each other.
	PerVoterDailyCap = 10
	// PerUserDailyCap limits total reputation an author can gain per 24h.
	PerUserDailyCap = 200
)

// Weight is a voter's influence, fixed at the moment they vote:
// 0 for new accounts, then 1 + log2(1 + rep/100), capped at MaxWeight.
// Logarithmic so established users count more without dominating.
func Weight(reputation int, accountAge time.Duration) float64 {
	if accountAge < MinAccountAge {
		return 0
	}
	if reputation < 0 {
		reputation = 0
	}
	return math.Min(MaxWeight, 1+math.Log2(1+float64(reputation)/100))
}

// Reward is the reputation a vote of value (+1/-1) moves for the target's
// author, before caps. Zero-weight voters move nothing.
func Reward(targetType string, value int, weight float64) int {
	if weight == 0 || value == 0 {
		return 0
	}
	if value < 0 {
		return DownvoteReward
	}
	if targetType == "thread" {
		return ThreadUpvote
	}
	return CommentUpvote
}

// Capped trims a positive reward so that neither the voter->author net for
// the last day nor the author's total for the last day exceeds its cap.
// Negative rewards are never capped.
func Capped(reward, fromVoterToday, authorToday int) int {
	if reward <= 0 {
		return reward
	}
	room := min(PerVoterDailyCap-fromVoterToday, PerUserDailyCap-authorToday)
	return max(0, min(reward, room))
}

// Gravity controls how fast threads sink in the "hot" ordering.
const Gravity = 1.5

// Hot is the time-decayed ranking score: weighted score divided by a power
// of age, so a strong new thread outranks an old one with the same score.
// The store computes the same expression in SQL; this is its reference.
func Hot(score float64, age time.Duration) float64 {
	return score / math.Pow(age.Hours()+2, Gravity)
}
