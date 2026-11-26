package routetest

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nuomiiiii/lite/nodemgt/store"
)

const (
	srsRuleItemQueryType     uint8 = 0
	srsRuleItemNetwork       uint8 = 1
	srsRuleItemDomain        uint8 = 2
	srsRuleItemDomainKeyword uint8 = 3
	srsRuleItemDomainRegex   uint8 = 4
	srsRuleItemSourceIPCIDR  uint8 = 5
	srsRuleItemIPCIDR        uint8 = 6
	srsRuleItemSourcePort    uint8 = 7
	srsRuleItemSourcePortRng uint8 = 8
	srsRuleItemPort          uint8 = 9
	srsRuleItemPortRange     uint8 = 10
	srsRuleItemFinal         uint8 = 0xFF
)

type ipRange struct {
	from netip.Addr
	to   netip.Addr
}

type ipRangeSet struct {
	ranges []ipRange
}

func (s *ipRangeSet) Contains(addr netip.Addr) bool {
	if s == nil || len(s.ranges) == 0 || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	i := sort.Search(len(s.ranges), func(i int) bool {
		return s.ranges[i].to.Compare(addr) >= 0
	})
	if i >= len(s.ranges) {
		return false
	}
	return s.ranges[i].from.Compare(addr) <= 0 && addr.Compare(s.ranges[i].to) <= 0
}

type srsRule struct {
	isLogical     bool
	logicalOr     bool
	invert        bool
	children      []srsRule
	domainMatcher *succinctDomainMatcher
	domainKeyword []string
	domainRegex   []*regexp.Regexp
	ipSet         *ipRangeSet
}

func (r *srsRule) matchDomain(domain string) bool {
	if r.isLogical {
		if len(r.children) == 0 {
			return false
		}
		var res bool
		if r.logicalOr {
			for i := range r.children {
				if r.children[i].matchDomain(domain) {
					res = true
					break
				}
			}
		} else {
			res = true
			for i := range r.children {
				if !r.children[i].matchDomain(domain) {
					res = false
					break
				}
			}
		}
		if r.invert {
			return !res
		}
		return res
	}
	matched := false
	if r.domainMatcher != nil && r.domainMatcher.Match(domain) {
		matched = true
	}
	if !matched {
		for _, kw := range r.domainKeyword {
			if kw != "" && strings.Contains(domain, kw) {
				matched = true
				break
			}
		}
	}
	if !matched {
		for _, re := range r.domainRegex {
			if re.MatchString(domain) {
				matched = true
				break
			}
		}
	}
	if r.invert {
		return !matched
	}
	return matched
}

func (r *srsRule) matchIP(addr netip.Addr) bool {
	if r.isLogical {
		if len(r.children) == 0 {
			return false
		}
		var res bool
		if r.logicalOr {
			for i := range r.children {
				if r.children[i].matchIP(addr) {
					res = true
					break
				}
			}
		} else {
			res = true
			for i := range r.children {
				if !r.children[i].matchIP(addr) {
					res = false
					break
				}
			}
		}
		if r.invert {
			return !res
		}
		return res
	}
	matched := r.ipSet != nil && r.ipSet.Contains(addr)
	if r.invert {
		return !matched
	}
	return matched
}

type SRSRuleSet struct {
	rules []srsRule
}

func (rs *SRSRuleSet) MatchDomain(domain string) bool {
	if rs == nil || domain == "" {
		return false
	}
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	for i := range rs.rules {
		if rs.rules[i].matchDomain(d) {
			return true
		}
	}
	return false
}

func (rs *SRSRuleSet) MatchIP(addr netip.Addr) bool {
	if rs == nil || !addr.IsValid() {
		return false
	}
	for i := range rs.rules {
		if rs.rules[i].matchIP(addr) {
			return true
		}
	}
	return false
}

func ParseSRS(data []byte) (*SRSRuleSet, error) {
	if len(data) < 4 || data[0] != 'S' || data[1] != 'R' || data[2] != 'S' {
		return nil, fmt.Errorf("invalid srs header")
	}
	zr, err := zlib.NewReader(bytes.NewReader(data[4:]))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	br := bufio.NewReader(zr)
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, err
	}
	out := &SRSRuleSet{rules: make([]srsRule, 0, n)}
	for range n {
		rule, err := readSRSRule(br, 0)
		if err != nil {
			return nil, err
		}
		out.rules = append(out.rules, rule)
	}
	return out, nil
}

func readSRSRule(br *bufio.Reader, depth int) (srsRule, error) {
	if depth > 8 {
		return srsRule{}, fmt.Errorf("srs rule nesting too deep")
	}
	mode, err := br.ReadByte()
	if err != nil {
		return srsRule{}, err
	}
	if mode == 1 {
		lmode, err := br.ReadByte()
		if err != nil {
			return srsRule{}, err
		}
		cnt, err := binary.ReadUvarint(br)
		if err != nil {
			return srsRule{}, err
		}
		r := srsRule{isLogical: true, logicalOr: lmode == 1, children: make([]srsRule, 0, cnt)}
		for range cnt {
			child, err := readSRSRule(br, depth+1)
			if err != nil {
				return srsRule{}, err
			}
			r.children = append(r.children, child)
		}
		inv, err := br.ReadByte()
		if err != nil {
			return srsRule{}, err
		}
		r.invert = inv != 0
		return r, nil
	}
	if mode != 0 {
		return srsRule{}, fmt.Errorf("unknown srs rule mode %d", mode)
	}
	var r srsRule
	for {
		itemType, err := br.ReadByte()
		if err != nil {
			return srsRule{}, err
		}
		switch itemType {
		case srsRuleItemDomain:
			m, err := readSuccinctDomainMatcher(br)
			if err != nil {
				return srsRule{}, err
			}
			r.domainMatcher = m
		case srsRuleItemDomainKeyword:
			kws, err := readStringSlice(br)
			if err != nil {
				return srsRule{}, err
			}
			for _, kw := range kws {
				r.domainKeyword = append(r.domainKeyword, strings.ToLower(kw))
			}
		case srsRuleItemDomainRegex:
			regs, err := readStringSlice(br)
			if err != nil {
				return srsRule{}, err
			}
			for _, raw := range regs {
				if re, compileErr := regexp.Compile(raw); compileErr == nil {
					r.domainRegex = append(r.domainRegex, re)
				}
			}
		case srsRuleItemIPCIDR:
			set, err := readIPRangeSet(br)
			if err != nil {
				return srsRule{}, err
			}
			r.ipSet = set
		case srsRuleItemSourceIPCIDR:
			if _, err := readIPRangeSet(br); err != nil {
				return srsRule{}, err
			}
		case srsRuleItemQueryType, srsRuleItemSourcePort, srsRuleItemPort:
			if err := skipUint16Slice(br); err != nil {
				return srsRule{}, err
			}
		case srsRuleItemNetwork, srsRuleItemSourcePortRng, srsRuleItemPortRange, 11, 12, 13, 14, 15, 17, 23:
			if _, err := readStringSlice(br); err != nil {
				return srsRule{}, err
			}
		case srsRuleItemFinal:
			inv, err := br.ReadByte()
			if err != nil {
				return srsRule{}, err
			}
			r.invert = inv != 0
			return r, nil
		default:
			return srsRule{}, fmt.Errorf("unsupported srs item type %d", itemType)
		}
	}
}

func readStringSlice(br *bufio.Reader) ([]string, error) {
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for range n {
		l, err := binary.ReadUvarint(br)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, l)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf))
	}
	return out, nil
}

func skipUint16Slice(br *bufio.Reader) error {
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return err
	}
	_, err = io.CopyN(io.Discard, br, int64(n)*2)
	return err
}

func readIPRangeSet(br *bufio.Reader) (*ipRangeSet, error) {
	ver, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if ver != 1 {
		return nil, fmt.Errorf("unsupported ipset version %d", ver)
	}
	var length uint64
	if err := binary.Read(br, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	ranges := make([]ipRange, 0, length)
	for range length {
		from, err := readIPAddr(br)
		if err != nil {
			return nil, err
		}
		to, err := readIPAddr(br)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, ipRange{from: from, to: to})
	}
	return &ipRangeSet{ranges: ranges}, nil
}

func readIPAddr(br *bufio.Reader) (netip.Addr, error) {
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return netip.Addr{}, err
	}
	if n != 4 && n != 16 {
		return netip.Addr{}, fmt.Errorf("invalid ip length %d", n)
	}
	var buf [16]byte
	if _, err := io.ReadFull(br, buf[:n]); err != nil {
		return netip.Addr{}, err
	}
	addr, ok := netip.AddrFromSlice(buf[:n])
	if !ok {
		return netip.Addr{}, fmt.Errorf("invalid ip bytes")
	}
	return addr.Unmap(), nil
}

// Succinct trie domain matcher compatible with sing/common/domain.Matcher.
const (
	prefixLabel = '\r'
	rootLabel   = '\n'
)

type succinctDomainMatcher struct {
	leaves, labelBitmap []uint64
	labels              []byte
	ranks, selects      []int32
}

func readSuccinctDomainMatcher(br *bufio.Reader) (*succinctDomainMatcher, error) {
	if _, err := br.ReadByte(); err != nil { // reserved byte
		return nil, err
	}
	leaves, err := readUint64Slice(br)
	if err != nil {
		return nil, err
	}
	labelBitmap, err := readUint64Slice(br)
	if err != nil {
		return nil, err
	}
	labelLen, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, err
	}
	labels := make([]byte, labelLen)
	if _, err := io.ReadFull(br, labels); err != nil {
		return nil, err
	}
	m := &succinctDomainMatcher{
		leaves:      leaves,
		labelBitmap: labelBitmap,
		labels:      labels,
	}
	m.selects, m.ranks = indexSelect32R64(m.labelBitmap)
	return m, nil
}

func readUint64Slice(br *bufio.Reader) ([]uint64, error) {
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, n)
	if n > 0 {
		if err := binary.Read(br, binary.BigEndian, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *succinctDomainMatcher) Match(domain string) bool {
	if m == nil || len(m.labelBitmap) == 0 || domain == "" {
		return false
	}
	key := reverseDomain(domain)
	var nodeId, bmIdx int
	for i := 0; i < len(key); i++ {
		currentChar := key[i]
		for ; ; bmIdx++ {
			if getBit(m.labelBitmap, bmIdx) != 0 {
				return false
			}
			nextLabel := m.labels[bmIdx-nodeId]
			if nextLabel == prefixLabel {
				return true
			}
			if nextLabel == rootLabel {
				nextNodeId := countZeros(m.labelBitmap, m.ranks, bmIdx+1)
				if currentChar == '.' && getBit(m.leaves, nextNodeId) != 0 {
					return true
				}
			}
			if nextLabel == currentChar {
				break
			}
		}
		nodeId = countZeros(m.labelBitmap, m.ranks, bmIdx+1)
		bmIdx = selectIthOne(m.labelBitmap, m.ranks, m.selects, nodeId-1) + 1
	}
	if getBit(m.leaves, nodeId) != 0 {
		return true
	}
	for ; ; bmIdx++ {
		if getBit(m.labelBitmap, bmIdx) != 0 {
			return false
		}
		nextLabel := m.labels[bmIdx-nodeId]
		if nextLabel == prefixLabel || nextLabel == rootLabel {
			return true
		}
	}
}

func reverseDomain(domain string) string {
	l := len(domain)
	b := make([]byte, l)
	for i := 0; i < l; {
		r, n := utf8.DecodeRuneInString(domain[i:])
		i += n
		utf8.EncodeRune(b[l-i:], r)
	}
	return string(b)
}

func getBit(bm []uint64, i int) uint64 {
	if i < 0 || i>>6 >= len(bm) {
		return 1
	}
	return bm[i>>6] & (1 << uint(i&63))
}

func countZeros(bm []uint64, ranks []int32, i int) int {
	a, _ := rank64(bm, ranks, int32(i))
	return i - int(a)
}

func selectIthOne(bm []uint64, ranks, selects []int32, i int) int {
	a, _ := select32R64(bm, selects, ranks, int32(i))
	return int(a)
}

func rank64(words []uint64, rindex []int32, i int32) (int32, int32) {
	wordI := i >> 6
	j := uint32(i & 63)
	n := rindex[wordI]
	w := words[wordI]
	c1 := n + int32(bits.OnesCount64(w&srsMask[j]))
	return c1, int32(w>>uint(j)) & 1
}

func indexRank64(words []uint64, trailing bool) []int32 {
	l := len(words)
	if trailing {
		l++
	}
	idx := make([]int32, l)
	n := int32(0)
	for i := 0; i < len(words); i++ {
		idx[i] = n
		n += int32(bits.OnesCount64(words[i]))
	}
	if trailing {
		idx[len(words)] = n
	}
	return idx
}

func select32R64(words []uint64, selectIndex, rankIndex []int32, i int32) (int32, int32) {
	a := int32(0)
	l := int32(len(words))
	wordI := selectIndex[i>>5] >> 6
	for ; rankIndex[wordI+1] <= i; wordI++ {
	}
	w := words[wordI]
	ww := w
	base := wordI << 6
	findIth := int(i - rankIndex[wordI])
	offset := int32(0)
	ones := bits.OnesCount32(uint32(ww))
	if ones <= findIth {
		findIth -= ones
		offset |= 32
		ww >>= 32
	}
	ones = bits.OnesCount16(uint16(ww))
	if ones <= findIth {
		findIth -= ones
		offset |= 16
		ww >>= 16
	}
	ones = bits.OnesCount8(uint8(ww))
	if ones <= findIth {
		a = int32(srsSelect8[(ww>>5)&(0x7f8)|uint64(findIth-ones)]) + offset + 8
	} else {
		a = int32(srsSelect8[(ww&0xff)<<3|uint64(findIth)]) + offset
	}
	a += base
	w &= srsRMaskUpto[a&63]
	if w != 0 {
		return a, base + int32(bits.TrailingZeros64(w))
	}
	wordI++
	for ; wordI < l; wordI++ {
		w = words[wordI]
		if w != 0 {
			return a, wordI<<6 + int32(bits.TrailingZeros64(w))
		}
	}
	return a, l << 6
}

func indexSelect32R64(words []uint64) ([]int32, []int32) {
	l := len(words) << 6
	sidx := make([]int32, 0, len(words))
	ith := -1
	for i := 0; i < l; i++ {
		if words[i>>6]&(1<<uint(i&63)) != 0 {
			ith++
			if ith&31 == 0 {
				sidx = append(sidx, int32(i))
			}
		}
	}
	return append(sidx[:0:0], sidx...), indexRank64(words, true)
}

var (
	srsMask      [65]uint64
	srsRMaskUpto [64]uint64
	srsSelect8   [256 * 8]uint8
)

func init() {
	for i := 0; i < 65; i++ {
		srsMask[i] = (1 << uint(i)) - 1
	}
	for i := 0; i < 64; i++ {
		srsRMaskUpto[i] = ^((1 << uint(i+1)) - 1)
	}
	for i := 0; i < 256; i++ {
		w := uint8(i)
		for j := 0; j < 8; j++ {
			x := bits.TrailingZeros8(w)
			w &= w - 1
			srsSelect8[i*8+j] = uint8(x)
		}
	}
}

// SRS cache loader for route testing
type cachedSRS struct {
	rs       *SRSRuleSet
	url      string
	loadedAt time.Time
}

var (
	srsCacheMu sync.Mutex
	srsCache   = map[string]cachedSRS{}
)

func srsCandidateURLs(st *store.Store, kind, code string) []string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	code = strings.ToLower(strings.TrimSpace(code))
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		for _, exist := range out {
			if exist == u {
				return
			}
		}
		out = append(out, u)
	}
	if st != nil {
		for _, src := range st.GeoRuleSources() {
			if src.Enabled && src.Kind == kind && src.Code == code {
				add(src.SRSURL)
				add(src.URL)
			}
		}
	}
	for _, src := range store.BuiltinGeoRuleSources() {
		if src.Kind == kind && src.Code == code {
			add(src.SRSURL)
		}
	}
	repo := "sing-geosite"
	if kind == "geoip" {
		repo = "sing-geoip"
	}
	escaped := url.PathEscape(kind + "-" + code + ".srs")
	add("https://raw.githubusercontent.com/SagerNet/" + repo + "/rule-set/" + escaped)
	add("https://cdn.jsdelivr.net/gh/SagerNet/" + repo + "@rule-set/" + escaped)
	return out
}

func loadSRSRuleSet(ctx context.Context, st *store.Store, kind, code string) (*SRSRuleSet, error) {
	urls := srsCandidateURLs(st, kind, code)
	if len(urls) == 0 {
		return nil, fmt.Errorf("no srs url for %s:%s", kind, code)
	}
	key := kind + ":" + code
	primaryURL := urls[0]

	srsCacheMu.Lock()
	if c, ok := srsCache[key]; ok && c.url == primaryURL && time.Since(c.loadedAt) < 6*time.Hour {
		srsCacheMu.Unlock()
		return c.rs, nil
	}
	srsCacheMu.Unlock()

	cacheDir := filepath.Join(os.TempDir(), "lite-plus-geo-cache")
	_ = os.MkdirAll(cacheDir, 0o700)
	diskPath := filepath.Join(cacheDir, kind+"-"+code+".srs")
	metaPath := diskPath + ".url"
	if metaBytes, err := os.ReadFile(metaPath); err == nil && strings.TrimSpace(string(metaBytes)) == primaryURL {
		if info, err := os.Stat(diskPath); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
			if raw, err := os.ReadFile(diskPath); err == nil {
				if rs, err := ParseSRS(raw); err == nil {
					srsCacheMu.Lock()
					srsCache[key] = cachedSRS{rs: rs, url: primaryURL, loadedAt: time.Now()}
					srsCacheMu.Unlock()
					return rs, nil
				}
			}
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Lite-Plus")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
			continue
		}
		rs, err := ParseSRS(body)
		if err != nil {
			lastErr = err
			continue
		}
		_ = os.WriteFile(diskPath, body, 0o600)
		_ = os.WriteFile(metaPath, []byte(primaryURL), 0o600)
		srsCacheMu.Lock()
		srsCache[key] = cachedSRS{rs: rs, url: primaryURL, loadedAt: time.Now()}
		srsCacheMu.Unlock()
		return rs, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("failed to load %s:%s", kind, code)
	}
	return nil, lastErr
}
