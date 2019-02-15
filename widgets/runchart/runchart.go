package runchart

import (
	"fmt"
	"github.com/sqshq/sampler/console"
	"github.com/sqshq/sampler/data"
	"image"
	"math"
	"strconv"
	"sync"
	"time"

	ui "github.com/sqshq/termui"
)

const (
	xAxisLabelsHeight = 1
	xAxisLabelsWidth  = 8
	xAxisLabelsIndent = 2
	xAxisGridWidth    = xAxisLabelsIndent + xAxisLabelsWidth
	yAxisLabelsHeight = 1
	yAxisLabelsIndent = 1

	historyReserveMin = 20

	xBrailleMultiplier = 2
	yBrailleMultiplier = 4
)

type Mode int

const (
	Default  Mode = 0
	Pinpoint Mode = 1
)

type RunChart struct {
	ui.Block
	lines     []TimeLine
	grid      ChartGrid
	timescale time.Duration
	mutex     *sync.Mutex
	mode      Mode
	selection time.Time
	precision int
	legend    Legend
}

type TimePoint struct {
	value      float64
	time       time.Time
	coordinate int
}

type TimeLine struct {
	points    []TimePoint
	extrema   ValueExtrema
	color     ui.Color
	label     string
	selection int
}

type TimeRange struct {
	max time.Time
	min time.Time
}

type ValueExtrema struct {
	max float64
	min float64
}

func NewRunChart(title string, precision int, refreshRateMs int, legend Legend) *RunChart {
	block := *ui.NewBlock()
	block.Title = title
	return &RunChart{
		Block:     block,
		lines:     []TimeLine{},
		timescale: calculateTimescale(refreshRateMs),
		mutex:     &sync.Mutex{},
		precision: precision,
		mode:      Default,
		legend:    legend,
	}
}

func (c *RunChart) newTimePoint(value float64) TimePoint {
	now := time.Now()
	return TimePoint{
		value:      value,
		time:       now,
		coordinate: c.calculateTimeCoordinate(now),
	}
}

func (c *RunChart) Draw(buffer *ui.Buffer) {

	c.mutex.Lock()
	c.Block.Draw(buffer)
	c.grid = c.newChartGrid()

	drawArea := image.Rect(
		c.Inner.Min.X+c.grid.minTimeWidth+1, c.Inner.Min.Y,
		c.Inner.Max.X, c.Inner.Max.Y-xAxisLabelsHeight-1,
	)

	c.renderAxes(buffer)
	c.renderLines(buffer, drawArea)
	c.renderLegend(buffer, drawArea)
	c.mutex.Unlock()
}

func (c *RunChart) ConsumeSample(sample data.Sample) {

	float, err := strconv.ParseFloat(sample.Value, 64)

	if err != nil {
		// TODO visual notification + check sample.Error
	}

	c.mutex.Lock()

	lineIndex := -1

	for i, line := range c.lines {
		if line.label == sample.Label {
			lineIndex = i
		}
	}

	if lineIndex == -1 {
		line := &TimeLine{
			points: []TimePoint{},
			color:  sample.Color,
			label:  sample.Label,
		}
		c.lines = append(c.lines, *line)
		lineIndex = len(c.lines) - 1
	}

	line := c.lines[lineIndex]
	timePoint := c.newTimePoint(float)
	line.points = append(line.points, timePoint)
	c.lines[lineIndex] = line

	c.trimOutOfRangeValues()
	c.mutex.Unlock()
}

func (c *RunChart) renderLines(buffer *ui.Buffer, drawArea image.Rectangle) {

	canvas := ui.NewCanvas()
	canvas.Rectangle = drawArea

	if len(c.lines) == 0 || len(c.lines[0].points) == 0 {
		return
	}

	selectionCoordinate := c.calculateTimeCoordinate(c.selection)
	selectionPoints := make(map[int]image.Point)

	probe := c.lines[0].points[0]
	delta := ui.AbsInt(c.calculateTimeCoordinate(probe.time) - probe.coordinate)

	for i, line := range c.lines {

		xPoint := make(map[int]image.Point)
		xOrder := make([]int, 0)

		if line.selection != 0 {
			line.selection -= delta
			c.lines[i].selection = line.selection
		}

		for j, timePoint := range line.points {

			timePoint.coordinate -= delta
			line.points[j] = timePoint

			var y int
			if c.grid.valueExtrema.max == c.grid.valueExtrema.min {
				y = (drawArea.Dy() - 2) / 2
			} else {
				valuePerY := (c.grid.valueExtrema.max - c.grid.valueExtrema.min) / float64(drawArea.Dy()-2)
				y = int(float64(timePoint.value-c.grid.valueExtrema.min) / valuePerY)
			}

			point := image.Pt(timePoint.coordinate, drawArea.Max.Y-y-1)

			if _, exists := xPoint[point.X]; exists {
				continue
			}

			if !point.In(drawArea) {
				continue
			}

			if line.selection == 0 {
				if len(line.points) > j+1 && ui.AbsInt(timePoint.coordinate-selectionCoordinate) > ui.AbsInt(line.points[j+1].coordinate-selectionCoordinate) {
					selectionPoints[i] = point
				}
			} else {
				if timePoint.coordinate == line.selection {
					selectionPoints[i] = point
				}
			}

			xPoint[point.X] = point
			xOrder = append(xOrder, point.X)
		}

		for i, x := range xOrder {

			currentPoint := xPoint[x]
			var previousPoint image.Point

			if i == 0 {
				previousPoint = currentPoint
			} else {
				previousPoint = xPoint[xOrder[i-1]]
			}

			canvas.Line(
				braillePoint(previousPoint),
				braillePoint(currentPoint),
				line.color,
			)
		}
	}

	canvas.Draw(buffer)

	if c.mode == Pinpoint {
		for lineIndex, point := range selectionPoints {
			buffer.SetCell(ui.NewCell(console.SymbolSelection, ui.NewStyle(c.lines[lineIndex].color)), point)
			if c.lines[lineIndex].selection == 0 {
				c.lines[lineIndex].selection = point.X
			}
		}
	}
}

func (c *RunChart) trimOutOfRangeValues() {

	minRangeTime := c.grid.timeRange.min.Add(-time.Minute * time.Duration(historyReserveMin))

	for i, item := range c.lines {
		lastOutOfRangeValueIndex := -1

		for j, point := range item.points {
			if point.time.Before(minRangeTime) {
				lastOutOfRangeValueIndex = j
			}
		}

		if lastOutOfRangeValueIndex > 0 {
			item.points = append(item.points[:0], item.points[lastOutOfRangeValueIndex+1:]...)
			c.lines[i] = item
		}
	}
}

func (c *RunChart) calculateTimeCoordinate(t time.Time) int {
	timeDeltaWithGridMaxTime := c.grid.timeRange.max.Sub(t).Nanoseconds()
	timeDeltaToPaddingRelation := float64(timeDeltaWithGridMaxTime) / float64(c.timescale.Nanoseconds())
	return c.grid.maxTimeWidth - int(math.Ceil(float64(xAxisGridWidth)*timeDeltaToPaddingRelation))
}

// TODO add boundaries for values in range
func (c *RunChart) getMaxValueLength() int {

	maxValueLength := 0

	for _, line := range c.lines {
		for _, point := range line.points {
			l := len(formatValue(point.value, c.precision))
			if l > maxValueLength {
				maxValueLength = l
			}
		}
	}

	return maxValueLength
}

func (c *RunChart) MoveSelection(shift int) {

	if c.mode == Default {
		c.mode = Pinpoint
		c.selection = getMidRangeTime(c.grid.timeRange)
		return
	} else {
		c.selection = c.selection.Add(c.grid.timePerPoint * time.Duration(shift))
		if c.selection.After(c.grid.timeRange.max) {
			c.selection = c.grid.timeRange.max
		} else if c.selection.Before(c.grid.timeRange.min) {
			c.selection = c.grid.timeRange.min
		}
	}

	for i := range c.lines {
		c.lines[i].selection = 0
	}
}

func (c *RunChart) DisableSelection() {
	if c.mode == Pinpoint {
		c.mode = Default
		return
	}
}

func getMidRangeTime(r TimeRange) time.Time {
	delta := r.max.Sub(r.min)
	return r.max.Add(-delta / 2)
}

func formatValue(value float64, precision int) string {
	format := "%." + strconv.Itoa(precision) + "f"
	return fmt.Sprintf(format, value)
}

// time duration between grid lines
func calculateTimescale(refreshRateMs int) time.Duration {

	multiplier := refreshRateMs * xAxisGridWidth / 2
	timescale := time.Duration(time.Millisecond * time.Duration(multiplier)).Round(time.Second)

	if timescale.Seconds() == 0 {
		return time.Second
	} else {
		return timescale
	}
}

func braillePoint(point image.Point) image.Point {
	return image.Point{X: point.X * xBrailleMultiplier, Y: point.Y * yBrailleMultiplier}
}