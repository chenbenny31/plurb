package store

import (
	"context"

	"github.com/lib/pq"

	"github.com/chenbenny31/plurb/internal/rep"
)

// RepLevels computes the current level cut points: the reputation at each
// of rep.LevelPercentiles among users with positive reputation. One
// aggregate query over users; callers cache the result.
func (s *Store) RepLevels(ctx context.Context) (rep.Levels, error) {
	p := rep.LevelPercentiles
	var cuts []int64
	var l rep.Levels
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*),
		       COALESCE(percentile_disc($1::float8[]) WITHIN GROUP (ORDER BY reputation), '{0,0,0,0}')
		FROM users WHERE reputation > 0`,
		pq.Array([]float64{p[0], p[1], p[2], p[3]})).Scan(&l.Users, pq.Array(&cuts))
	if err != nil {
		return rep.Levels{}, err
	}
	for i := 0; i < len(l.Cuts) && i < len(cuts); i++ {
		l.Cuts[i] = int(cuts[i])
	}
	return l, nil
}

// RepTopPercent returns how high a reputation ranks, as "top N%" among users
// with positive reputation (1 = the very top). 0 means no reputation.
func (s *Store) RepTopPercent(ctx context.Context, reputation int) (int, error) {
	if reputation <= 0 {
		return 0, nil
	}
	var above, total int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE reputation > $1), count(*)
		FROM users WHERE reputation > 0`, reputation).Scan(&above, &total)
	if err != nil || total == 0 {
		return 0, err
	}
	// ceil((above+1) / total * 100)
	return ((above+1)*100 + total - 1) / total, nil
}
