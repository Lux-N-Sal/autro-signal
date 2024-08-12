package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"

	"github.com/segmentio/kafka-go"
)

type CandleData struct {
	OpenTime                 int64
	Open, High, Low, Close   string
	Volume                   string
	CloseTime                int64
	QuoteAssetVolume         string
	NumberOfTrades           int
	TakerBuyBaseAssetVolume  string
	TakerBuyQuoteAssetVolume string
}

type TechnicalIndicators struct {
	EMA200       float64
	MACD         float64
	Signal       float64
	ParabolicSAR float64
}

type SignalCondition struct {
	Condition bool
	Value     float64
}

type SignalConditions struct {
	Long  [3]SignalCondition
	Short [3]SignalCondition
}

type SignalResult struct {
	Signal     string
	Timestamp  int64
	Price      string
	Conditions SignalConditions
}

var (
	kafkaBroker              string
	kafkaTopicFromPrice      string
	kafkaTopicToNotification string
)

func init() {
	kafkaBroker = os.Getenv("KAFKA_BROKER")
	if kafkaBroker == "" {
		kafkaBroker = "kafka:9092"
	}

	kafkaTopicFromPrice = os.Getenv("KAFKA_TOPIC_FROM_PRICE")
	if kafkaTopicFromPrice == "" {
		kafkaTopicFromPrice = "price-to-signal"
	}
	kafkaTopicToNotification = os.Getenv("KAFKA_TOPIC_TO_NOTIFICATION")
	if kafkaTopicToNotification == "" {
		kafkaTopicToNotification = "signal-to-notification"
	}
}

// / consumer와 연결함수
func createReader() *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{kafkaBroker},
		Topic:       kafkaTopicFromPrice,
		MaxAttempts: 5,
	})
}

func createWriter() *kafka.Writer {
	return kafka.NewWriter(kafka.WriterConfig{
		Brokers:     []string{kafkaBroker},
		Topic:       kafkaTopicToNotification,
		MaxAttempts: 5,
	})
}

func writeToKafka(writer *kafka.Writer, signalReuslt SignalResult) error {
	value, err := json.Marshal(signalReuslt)
	if err != nil {
		return fmt.Errorf("error marshalling signal result: %v", err)
	}

	err = writer.WriteMessages(context.Background(), kafka.Message{
		Value: value,
	})

	return err
}

// EMA 계산 float 반환
func calculateEMA(prices []float64, period int) float64 {
	k := 2.0 / float64(period+1)
	ema := prices[0]
	for i := 1; i < len(prices); i++ {
		ema = prices[i]*k + ema*(1-k)
	}
	return ema
}

// EMA 계산 slice 반환
func calculateEMASlice(prices []float64, period int) []float64 {
	k := 2.0 / float64(period+1)
	ema := make([]float64, len(prices))
	ema[0] = prices[0]
	for i := 1; i < len(prices); i++ {
		ema[i] = prices[i]*k + ema[i-1]*(1-k)
	}
	return ema
}

// / MACD 계산
func calculateMACD(prices []float64) (float64, float64) {
	if len(prices) < 26 {
		return 0, 0 // Not enough data
	}

	ema12 := calculateEMA(prices, 12)
	ema26 := calculateEMA(prices, 26)
	macd := ema12 - ema26

	ema12Slice := calculateEMASlice(prices, 12)
	ema26Slice := calculateEMASlice(prices, 26)
	macdSlice := make([]float64, len(prices))
	for i := 0; i < len(prices); i++ {
		macdSlice[i] = ema12Slice[i] - ema26Slice[i]
	}

	signal := calculateEMA(macdSlice, 9)
	return macd, signal
}

// / Parabolic SAR 계산
func calculateParabolicSAR(highs, lows []float64) float64 {
	af := 0.02
	maxAf := 0.2
	sar := lows[0]
	ep := highs[0]
	isLong := true

	for i := 1; i < len(highs); i++ {
		if isLong {
			sar = sar + af*(ep-sar)
			if highs[i] > ep {
				ep = highs[i]
				af = math.Min(af+0.02, maxAf)
			}
			if sar > lows[i] {
				isLong = false
				sar = ep
				ep = lows[i]
				af = 0.02
			}
		} else {
			sar = sar - af*(sar-ep)
			if lows[i] < ep {
				ep = lows[i]
				af = math.Min(af+0.02, maxAf)
			}
			if sar < highs[i] {
				isLong = true
				sar = ep
				ep = highs[i]
				af = 0.02
			}
		}
	}
	return sar
}

// / 보조 지표 계산
func calculateIndicators(candles []CandleData) (TechnicalIndicators, error) {
	if len(candles) < 300 {
		return TechnicalIndicators{}, fmt.Errorf("insufficient data: need at least 300 candles, got %d", len(candles))
	}

	prices := make([]float64, len(candles))
	highs := make([]float64, len(candles))
	lows := make([]float64, len(candles))

	for i, candle := range candles {
		price, err := strconv.ParseFloat(candle.Close, 64)
		if err != nil {
			return TechnicalIndicators{}, fmt.Errorf("error parsing close price: %v", err)
		}
		prices[i] = price

		high, err := strconv.ParseFloat(candle.High, 64)
		if err != nil {
			return TechnicalIndicators{}, fmt.Errorf("error parsing high price: %v", err)
		}
		highs[i] = high

		low, err := strconv.ParseFloat(candle.Low, 64)
		if err != nil {
			return TechnicalIndicators{}, fmt.Errorf("error parsing low price: %v", err)
		}
		lows[i] = low
	}

	ema200 := calculateEMA(prices, 200)
	macd, signal := calculateMACD(prices)
	parabolicSAR := calculateParabolicSAR(highs, lows)

	return TechnicalIndicators{
		EMA200:       ema200,
		MACD:         macd,
		Signal:       signal,
		ParabolicSAR: parabolicSAR,
	}, nil
}

// signal 생성 함수
func generateSignal(candles []CandleData, indicators TechnicalIndicators) (string, SignalConditions) {
	lastPrice, _ := strconv.ParseFloat(candles[len(candles)-1].Close, 64)
	lastHigh, _ := strconv.ParseFloat(candles[len(candles)-1].High, 64)
	lastLow, _ := strconv.ParseFloat(candles[len(candles)-1].Low, 64)

	conditions := SignalConditions{
		Long: [3]SignalCondition{
			{Condition: lastPrice > indicators.EMA200, Value: lastPrice - indicators.EMA200},
			{Condition: indicators.MACD > indicators.Signal, Value: indicators.MACD - indicators.Signal},
			{Condition: indicators.ParabolicSAR < lastLow, Value: lastLow - indicators.ParabolicSAR},
		},
		Short: [3]SignalCondition{
			{Condition: lastPrice < indicators.EMA200, Value: lastPrice - indicators.EMA200},
			{Condition: indicators.MACD < indicators.Signal, Value: indicators.MACD - indicators.Signal},
			{Condition: indicators.ParabolicSAR > lastHigh, Value: indicators.ParabolicSAR - lastHigh},
		},
	}

	if conditions.Long[0].Condition && conditions.Long[1].Condition && conditions.Long[2].Condition {
		return "LONG", conditions
	} else if conditions.Short[0].Condition && conditions.Short[1].Condition && conditions.Short[2].Condition {
		return "SHORT", conditions
	}
	return "NO SIGNAL", conditions
}

func main() {
	log.Println("Starting Signal Service...")

	reader := createReader()
	defer reader.Close()

	writer := createWriter()
	defer writer.Close()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)

	log.Println("Signal Service is now running. Press CTRL-C to exit.")

	for {
		select {
		case <-signals:
			log.Println("Interrupt is detected. Gracefully shutting down...")
			return

		default:
			msg, err := reader.ReadMessage(context.Background())
			if err != nil {
				log.Printf("Error reading message: %v\n", err)
			}

			var candles []CandleData
			if err := json.Unmarshal(msg.Value, &candles); err != nil {
				log.Printf("Error unmarshalling message: %v\n", err)
				continue
			}

			if len(candles) == 300 {
				indicators, err := calculateIndicators(candles)
				if err != nil {
					log.Printf("Error calculating indicators: %v\n", err)
					continue
				}

				signalType, conditions := generateSignal(candles, indicators)
				lastCandle := candles[len(candles)-1]

				signalResult := SignalResult{
					Signal:     signalType,
					Timestamp:  lastCandle.CloseTime,
					Price:      lastCandle.Close,
					Conditions: conditions,
				}

				err = writeToKafka(writer, signalResult)

				if err != nil {
					log.Printf("Error sending signal to API Gateway: %v", err)
				} else {
					log.Printf("Signal sent to API Gateway. Response: %v", signalResult)
				}
			}
		}
	}
}
