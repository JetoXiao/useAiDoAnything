package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type announcementRepoStub struct {
	item *Announcement
}

func (s *announcementRepoStub) Create(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

func (s *announcementRepoStub) GetByID(_ context.Context, _ int64) (*Announcement, error) {
	if s.item == nil {
		return nil, ErrAnnouncementNotFound
	}
	return s.item, nil
}

func (s *announcementRepoStub) Update(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

func (*announcementRepoStub) Delete(context.Context, int64) error {
	return nil
}

func (*announcementRepoStub) List(context.Context, pagination.PaginationParams, AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (*announcementRepoStub) ListActive(context.Context, time.Time) ([]Announcement, error) {
	return nil, nil
}

type announcementStatsUserRepoStub struct {
	UserRepository
	users []User
}

func (s *announcementStatsUserRepoStub) ListWithFilters(_ context.Context, params pagination.PaginationParams, _ UserListFilters) ([]User, *pagination.PaginationResult, error) {
	if params.Page > 1 {
		return []User{}, &pagination.PaginationResult{
			Total:    int64(len(s.users)),
			Page:     params.Page,
			PageSize: params.PageSize,
			Pages:    1,
		}, nil
	}
	return s.users, &pagination.PaginationResult{
		Total:    int64(len(s.users)),
		Page:     1,
		PageSize: params.PageSize,
		Pages:    1,
	}, nil
}

type announcementStatsReadRepoStub struct {
	AnnouncementReadRepository
	readByUser map[int64]time.Time
}

func (s *announcementStatsReadRepoStub) GetReadMapByUsers(_ context.Context, _ int64, userIDs []int64) (map[int64]time.Time, error) {
	out := make(map[int64]time.Time)
	for _, userID := range userIDs {
		if readAt, ok := s.readByUser[userID]; ok {
			out[userID] = readAt
		}
	}
	return out, nil
}

func TestAnnouncementServiceGetReadStatsCountsOnlyMatchingUsers(t *testing.T) {
	repo := &announcementRepoStub{
		item: &Announcement{
			ID:     1,
			Title:  "公告",
			Status: AnnouncementStatusActive,
			Targeting: AnnouncementTargeting{
				AnyOf: []AnnouncementConditionGroup{{
					AllOf: []AnnouncementCondition{{
						Type:     AnnouncementConditionTypeBalance,
						Operator: AnnouncementOperatorGTE,
						Value:    100,
					}},
				}},
			},
		},
	}
	users := &announcementStatsUserRepoStub{
		users: []User{
			{ID: 1, Balance: 120},
			{ID: 2, Balance: 100},
			{ID: 3, Balance: 20},
		},
	}
	reads := &announcementStatsReadRepoStub{
		readByUser: map[int64]time.Time{
			2: time.Now(),
			3: time.Now(), // not eligible; must not affect the read count
		},
	}
	svc := NewAnnouncementService(repo, reads, users, nil)

	stats, err := svc.GetReadStats(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.EligibleUsers)
	require.Equal(t, int64(1), stats.ReadUsers)
	require.Equal(t, int64(1), stats.UnreadUsers)
}

func TestAnnouncementServiceCreateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)

	_, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:      "公告",
		Content:    "内容",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModePopup,
		StartsAt:   &now,
		EndsAt:     &now,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}

func TestAnnouncementServiceUpdateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{
		item: &Announcement{
			ID:         1,
			Title:      "公告",
			Content:    "内容",
			Status:     AnnouncementStatusActive,
			NotifyMode: AnnouncementNotifyModePopup,
		},
	}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)
	startsAt := &now
	endsAt := &now

	_, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{
		StartsAt: &startsAt,
		EndsAt:   &endsAt,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}
