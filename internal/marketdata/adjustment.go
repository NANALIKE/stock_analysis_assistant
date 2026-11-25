package marketdata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

const AdjustmentAlgorithm = "back-cumulative-rational-half-up-v1"

type Factor struct {
	Date  string `json:"date"`
	Fore  string `json:"fore"`
	Back  string `json:"back"`
	Value string `json:"value"`
}

func Decimal(value string) (*big.Rat, error) {
	// Provider decimals, not arbitrary rational expressions.
	if value == "" || strings.ContainsAny(value, "/eE") {
		return nil, fmt.Errorf("invalid decimal factor")
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("factor must be a positive decimal")
	}
	return r, nil
}

func RoundPrice(value *big.Rat) (int64, error) {
	if value.Sign() < 0 {
		return 0, fmt.Errorf("negative adjusted price")
	}
	n := new(big.Int).Mul(value.Num(), big.NewInt(2))
	n.Add(n, value.Denom())
	d := new(big.Int).Mul(value.Denom(), big.NewInt(2))
	n.Quo(n, d)
	if !n.IsInt64() {
		return 0, fmt.Errorf("adjusted price overflows int64")
	}
	return n.Int64(), nil
}

func ValidateFactors(factors []Factor) error {
	for index, f := range factors {
		if index > 0 && factors[index-1].Date >= f.Date {
			return fmt.Errorf("factor dates must be unique and ordered")
		}
		for _, value := range []string{f.Fore, f.Back, f.Value} {
			if _, err := Decimal(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func BackAt(factors []Factor, day string) string {
	index := sort.Search(len(factors), func(i int) bool { return factors[i].Date > day })
	if index == 0 {
		return "1"
	}
	return factors[index-1].Back
}

func Adjust(bar FixedBar, factors []Factor, anchor string) (FixedBar, error) {
	at, err := Decimal(BackAt(factors, bar.TradeDate))
	if err != nil {
		return FixedBar{}, err
	}
	end, err := Decimal(BackAt(factors, anchor))
	if err != nil {
		return FixedBar{}, err
	}
	ratio := new(big.Rat).Quo(at, end)
	result := bar
	result.Adjustment = "qfq"
	for _, pair := range []struct {
		in  int64
		out *int64
	}{
		{bar.Open, &result.Open}, {bar.High, &result.High}, {bar.Low, &result.Low}, {bar.Close, &result.Close},
	} {
		value, err := RoundPrice(new(big.Rat).Mul(new(big.Rat).SetInt64(pair.in), ratio))
		if err != nil {
			return FixedBar{}, err
		}
		*pair.out = value
	}
	return result, nil
}

func FactorHash(factors []Factor, anchor string) string {
	body, _ := json.Marshal(struct {
		Factors   []Factor
		Anchor    string
		Algorithm string
	}{factors, anchor, AdjustmentAlgorithm})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type interval struct{ lo, hi *big.Rat }

func exact(r *big.Rat) interval { return interval{new(big.Rat).Set(r), new(big.Rat).Set(r)} }
func decimalInterval(value string, uncertain bool) (interval, error) {
	r, err := Decimal(value)
	if err != nil {
		return interval{}, err
	}
	if !uncertain {
		return exact(r), nil
	}
	digits := 0
	if dot := strings.IndexByte(value, '.'); dot >= 0 {
		digits = len(value) - dot - 1
	}
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	unit.Mul(unit, big.NewInt(2))
	e := new(big.Rat).SetFrac(big.NewInt(1), unit)
	lo := new(big.Rat).Sub(r, e)
	if lo.Sign() <= 0 {
		return interval{}, fmt.Errorf("factor uncertainty includes zero")
	}
	return interval{lo, new(big.Rat).Add(r, e)}, nil
}
func mul(a, b interval) interval {
	return interval{new(big.Rat).Mul(a.lo, b.lo), new(big.Rat).Mul(a.hi, b.hi)}
}
func div(a, b interval) interval {
	return interval{new(big.Rat).Quo(a.lo, b.hi), new(big.Rat).Quo(a.hi, b.lo)}
}
func factorInterval(factors []Factor, day string) (interval, error) {
	i := sort.Search(len(factors), func(i int) bool { return factors[i].Date > day })
	if i == 0 {
		return exact(big.NewRat(1, 1)), nil
	}
	return decimalInterval(factors[i-1].Back, true)
}

func forePrecision(factors []Factor, day string) (interval, error) {
	if len(factors) == 0 {
		return exact(big.NewRat(1, 1)), nil
	}
	i := sort.Search(len(factors), func(i int) bool { return factors[i].Date > day })
	if i == 0 {
		i = 1
	}
	value := factors[i-1].Fore
	bounds, err := decimalInterval(value, true)
	if err != nil {
		return interval{}, err
	}
	nominal, err := Decimal(value)
	if err != nil {
		return interval{}, err
	}
	return div(bounds, exact(nominal)), nil
}

type AdjustmentDifference struct {
	Date   string `json:"date"`
	Field  string `json:"field"`
	Local  int64  `json:"local_1e4"`
	Lower  string `json:"supplier_normalized_lower_1e4"`
	Upper  string `json:"supplier_normalized_upper_1e4"`
	Passed bool   `json:"passed"`
}

// VerifyExchangeReference checks the cumulative factor step against the
// unadjusted ex-date reference. It uses factor precision, not a percent margin.
func VerifyExchangeReference(previous, current FixedBar, factors []Factor) (AdjustmentDifference, error) {
	d := AdjustmentDifference{Date: current.TradeDate, Field: "exchange_previous_close"}
	if current.PreviousClose == nil {
		return d, fmt.Errorf("ex-date reference missing")
	}
	before, err := factorInterval(factors, previous.TradeDate)
	if err != nil {
		return d, err
	}
	after, err := factorInterval(factors, current.TradeDate)
	if err != nil {
		return d, err
	}
	bounds := mul(exact(new(big.Rat).SetInt64(previous.Close)), div(before, after))
	lo := new(big.Rat).Sub(bounds.lo, big.NewRat(1, 1))
	hi := new(big.Rat).Add(bounds.hi, big.NewRat(1, 1))
	actual := new(big.Rat).SetInt64(*current.PreviousClose)
	d.Local = *current.PreviousClose
	d.Lower = lo.FloatString(8)
	d.Upper = hi.FloatString(8)
	d.Passed = actual.Cmp(lo) >= 0 && actual.Cmp(hi) <= 0
	return d, nil
}

// VerifySample normalizes the supplier's present qfq at a reference date within
// the sample window. Prices are in 1e-4 CNY; factor and supplier rounding errors
// are propagated with positive rational intervals, never a percent tolerance.
func VerifySample(bar, reference FixedBar, supplier, supplierReference map[string]string, factors []Factor, anchor string) ([]AdjustmentDifference, error) {
	local, err := Adjust(bar, factors, anchor)
	if err != nil {
		return nil, err
	}
	at, err := factorInterval(factors, reference.TradeDate)
	if err != nil {
		return nil, err
	}
	end, err := factorInterval(factors, anchor)
	if err != nil {
		return nil, err
	}
	bt, err := factorInterval(factors, bar.TradeDate)
	if err != nil {
		return nil, err
	}
	nominalAt, _ := Decimal(BackAt(factors, bar.TradeDate))
	nominalEnd, _ := Decimal(BackAt(factors, anchor))
	// Convert supplier-normalized uncertainty back to the nominal local ratio.
	localUncertainty := div(div(bt, end), exact(new(big.Rat).Quo(nominalAt, nominalEnd)))
	// Supplier qfq uses rounded fore factors. Their relative precision also
	// affects the sample/reference ratio, even after removing today's anchor.
	foreAt, err := forePrecision(factors, bar.TradeDate)
	if err != nil {
		return nil, err
	}
	foreRef, err := forePrecision(factors, reference.TradeDate)
	if err != nil {
		return nil, err
	}
	localUncertainty = mul(localUncertainty, div(foreAt, foreRef))
	differences := []AdjustmentDifference{}
	for _, p := range []struct {
		name       string
		local, ref int64
	}{
		{"open", local.Open, reference.Open}, {"high", local.High, reference.High},
		{"low", local.Low, reference.Low}, {"close", local.Close, reference.Close},
	} {
		// Normalize every field by the reference CLOSE to avoid OHLC-specific scaling.
		s, _err := decimalInterval(supplier[p.name], true)
		if _err != nil {
			return nil, _err
		}
		r, _err := decimalInterval(supplierReference["close"], true)
		if _err != nil {
			return nil, _err
		}
		bounds := mul(div(s, r), exact(new(big.Rat).SetInt64(reference.Close)))
		bounds = mul(bounds, div(at, end))
		bounds = div(bounds, localUncertainty)
		// One stored tick accounts for the final local and supplier rounding.
		lo := new(big.Rat).Sub(bounds.lo, big.NewRat(1, 1))
		hi := new(big.Rat).Add(bounds.hi, big.NewRat(1, 1))
		actual := new(big.Rat).SetInt64(p.local)
		differences = append(differences, AdjustmentDifference{
			Date: bar.TradeDate, Field: p.name, Local: p.local, Lower: lo.FloatString(8), Upper: hi.FloatString(8),
			Passed: actual.Cmp(lo) >= 0 && actual.Cmp(hi) <= 0,
		})
	}
	return differences, nil
}
