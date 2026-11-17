// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ysicing/51code/internal/gitrepo"
	"github.com/ysicing/51code/locales"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/chart"
)

func (app *nativeApp) insightsPanel(c *ui.Context) {
	state := app.insights
	if state == nil {
		return
	}
	ui.Scroll(c).Key("repository-insights").Fill().Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(24, 28).Gap(20).Children(func() {
			ui.Row(c).Wrap().FillWidth().Gap(14).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Gap(5).Children(func() {
					ui.Text(c, locales.Insights()).FontSize(23).Bold()
					ui.Text(c, state.from.Format("2006/01/02")+" — "+gitrepo.InsightsDayStart(state.until, -1).Format("2006/01/02")).
						FontSize(12).TextColor(c.Theme().TextMuted)
				})
				app.insightsControls(c)
			})
			if app.readTask != nil && app.readTask.kind == nativeReadInsights {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, locales.InsightsLoading()).FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Button(c, locales.Cancel()).OnClick(app.cancelRead)
				})
			}
			if state.problem != "" {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					app.inlineProblem(c, state.problem).Grow(1).MinWidth(0)
					ui.Button(c, locales.Retry()).OnClick(func() { app.refreshInsights(true) })
				})
			}
			if state.result == nil {
				return
			}
			result := state.result
			app.insightsSummary(c, result)
			ui.Divider(c)
			if result.Shallow {
				ui.Text(c, locales.InsightsShallow()).FontSize(12).TextColor(c.Theme().Warning)
			}
			if result.Commits == 0 {
				ui.Text(c, locales.InsightsEmpty()).TextColor(c.Theme().TextMuted).Margin(30, 0)
				return
			}
			width, _ := c.Size()
			columns := 2
			if width-app.prefs.Sidebar < 700 {
				columns = 1
			}
			ui.Grid(c).Columns(columns).FillWidth().GapY(24).GapX(32).Children(func() {
				insightsDailyChart(c, result.Days, false)
				insightsDailyChart(c, result.Days, true)
			})
			ui.Divider(c)
			ui.Grid(c).Columns(columns).FillWidth().GapY(24).GapX(32).Children(func() {
				insightsRanking(c, result, false)
				insightsRanking(c, result, true)
			})
		})
	})
}

func (app *nativeApp) insightsControls(c *ui.Context) {
	state := app.insights
	refs := app.insightsRefs()
	labels := make([]string, len(refs))
	for index, ref := range refs {
		labels[index] = strings.TrimPrefix(ref, "refs/heads/")
		if ref == "HEAD" {
			labels[index] = locales.CurrentBranch() + " · " + app.repository.snapshot.Branch
		}
	}
	selected := labels[max(0, slices.Index(refs, state.ref))]
	c.Key("insights-branch")
	ui.Select(c, &selected, labels).Width(190).Label(locales.Branch()).Tooltip(selected).OnChange(func() {
		index := slices.Index(labels, selected)
		if index >= 0 {
			state.ref, state.result, state.checked = refs[index], nil, time.Time{}
			app.refreshInsights(false)
		}
	})
	periods := []string{"7", "30", "90"}
	index := max(0, slices.Index(periods, state.period))
	c.Key("insights-period")
	ui.Segmented(c, &index, locales.InsightsDays("7"), locales.InsightsDays("30"), locales.InsightsDays("90")).
		Label(locales.InsightsPeriod()).OnChange(func() {
		state.period = periods[index]
		app.refreshInsights(false)
	})
}

func (app *nativeApp) insightsSummary(c *ui.Context, result *gitrepo.RepositoryInsights) {
	labels := []string{locales.InsightsCommits(), locales.InsightsContributors(), locales.InsightsActiveDays(), locales.InsightsFiles()}
	values := []int{result.Commits, result.Contributors, result.ActiveDays, result.ChangedFiles}
	ui.Grid(c).Columns(4).FillWidth().Margin(6, 0, 2).Children(func() {
		for index, label := range labels {
			column := ui.Column(c).Gap(4).MinWidth(0)
			if index > 0 {
				column.BorderWidth(0, 0, 0, 1).BorderColor(c.Theme().Border).Padding(0, 0, 0, 24)
			}
			column.Children(func() {
				ui.Text(c, strconv.Itoa(values[index])).FontSize(30).Bold()
				ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(1)
			})
		}
	})
}

func insightsDailyChart(c *ui.Context, days []gitrepo.InsightsDay, churn bool) {
	theme := c.Theme()
	title := locales.InsightsActivity()
	if churn {
		title = locales.InsightsChurn()
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(18).Children(func() {
		ui.Row(c).Wrap().FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).Bold().FontSize(15).Grow(1)
			if churn {
				var added, deleted int64
				for _, day := range days {
					added += day.Added
					deleted += day.Deleted
				}
				ui.Text(c, fmt.Sprintf("%s +%s", locales.InsightsAdded(), insightsNumber(added))).FontSize(11).TextColor(theme.Success)
				ui.Text(c, fmt.Sprintf("%s −%s", locales.InsightsDeleted(), insightsNumber(deleted))).FontSize(11).TextColor(theme.Danger)
			}
		})
		labels := make([]string, len(days))
		hi, lo := float64(1), float64(0)
		for index, day := range days {
			date, err := time.Parse("2006-01-02", day.Date)
			if err == nil {
				labels[index] = date.Format("01/02")
			}
			if churn {
				hi = max(hi, float64(day.Added))
				lo = min(lo, -float64(day.Deleted))
			} else {
				hi = max(hi, float64(day.Commits))
			}
		}
		x := chart.NewBand(labels...).Padding(0.35, 0.2)
		y := chart.NewLinear(lo, hi).Nice(4)
		ticks := y.Ticks(4)
		ticks = slices.DeleteFunc(ticks, func(tick chart.Tick) bool {
			return tick.Value != float64(int64(tick.Value))
		})
		for index := range ticks {
			ticks[index].Label = insightsNumber(int64(ticks[index].Value))
		}
		bottom := []chart.Tick{}
		step := max(1, (len(days)-1+5)/6)
		for index := 0; index < len(days)-1; index += step {
			if len(days)-1-index < step/2 && index != len(days)-1 {
				continue
			}
			bottom = append(bottom, chart.Tick{At: x.At(index), Label: strings.ReplaceAll(labels[index], "-", "/")})
		}
		if len(days) > 0 {
			bottom = append(bottom, chart.Tick{At: x.At(len(days) - 1), Label: strings.ReplaceAll(labels[len(days)-1], "-", "/")})
		}
		axes := chart.Axes{Left: ticks, Bottom: bottom, Label: func(_ chart.Side, tick chart.Tick) {
			ui.Text(c, tick.Label).NoWrap().FontSize(10).TextColor(theme.TextMuted)
		}}
		chart.Plot(c, axes, func(area ui.Element) {
			area.Draw(func(painter *ui.Painter, rect ui.Rect) {
				for _, tick := range ticks {
					painter.Line(rect.X, chart.Y(rect, tick.At), rect.X+rect.W, chart.Y(rect, tick.At), 1, theme.Border.Alpha(0.65))
				}
			})
			for index, day := range days {
				if churn {
					label := fmt.Sprintf("%s · +%d / −%d", day.Date, day.Added, day.Deleted)
					chart.Rect(c, x.Band(index), y.Span(0, float64(day.Added))).Background(theme.Success).Label(label).Tooltip(label)
					chart.Rect(c, x.Band(index), y.Span(0, -float64(day.Deleted))).Background(theme.Danger).Label(label).Tooltip(label)
				} else {
					label := fmt.Sprintf("%s · %s %d", day.Date, locales.InsightsCommits(), day.Commits)
					chart.Rect(c, x.Band(index), y.Span(0, float64(day.Commits))).Background(theme.Accent).Label(label).Tooltip(label)
				}
			}
		}).Label(title).Height(190).FillWidth().Gap(8).Padding(5, 12, 0, 6)
	})
}

func insightsNumber(value int64) string {
	if value >= 10000 {
		return fmt.Sprintf("%.1fk", float64(value)/1000)
	}
	return strconv.FormatInt(value, 10)
}

func insightsRanking(c *ui.Context, result *gitrepo.RepositoryInsights, files bool) {
	title, countLabel := locales.InsightsContributors(), locales.InsightsCommits()
	length := len(result.Authors)
	if files {
		title, countLabel, length = locales.InsightsHotspots(), locales.InsightsTouchCount(), len(result.Files)
	}
	ui.Column(c).FillWidth().MinWidth(0).Gap(14).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).Bold().FontSize(15).Grow(1)
			ui.Text(c, countLabel).FontSize(11).TextColor(c.Theme().TextMuted)
		})
		if length == 0 {
			ui.Text(c, locales.InsightsNoFiles()).FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		maximum := 1
		if files {
			maximum = result.Files[0].Commits
		} else {
			maximum = result.Authors[0].Commits
		}
		ui.Grid(c).ColumnTracks(ui.Fr(1.5), ui.Fr(1), ui.Fixed(30)).GapY(10).GapX(12).FillWidth().Children(func() {
			for index := range length {
				label, tooltip, value := "", "", 0
				if files {
					label, value = result.Files[index].Path, result.Files[index].Commits
					tooltip = label
				} else {
					author := result.Authors[index]
					label, tooltip, value = author.Name, author.Name+" <"+author.Email+">", author.Commits
					if label == "" {
						label = author.Email
					}
				}
				ui.Text(c, label).FontSize(12).SingleLine().MinWidth(0).Tooltip(tooltip)
				fraction := float32(value) / float32(max(1, maximum))
				chart.Plot(c, chart.Axes{}, func(_ ui.Element) {
					chart.Rect(c, chart.Span{Lo: 0, Hi: fraction}, chart.Span{Lo: 0.15, Hi: 0.85}).
						Background(c.Theme().Accent).Tooltip(fmt.Sprintf("%s · %d", label, value))
				}).Height(20).FillWidth()
				ui.Text(c, strconv.Itoa(value)).FontSize(12)
			}
		})
	})
}
