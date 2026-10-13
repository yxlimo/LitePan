package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// seedMedia 用 ffmpeg 造一批贴近真实网盘命名与目录结构的媒体文件。
//
// 命名按真实 PT 组的习惯拼装：中文名 + 英文名（或韩文名）、来源、分辨率、
// 编码、音频与制作组，季号可能出现在目录名、文件名或两者都有，也可能都没有。
// 结构上覆盖：带目录、不带目录、纯文件名、方括号包中文名、全角括号目录、
// 多部合集。
//
// 不生成 nfo：nfo 属于「按元数据跳过刮削」这类特殊流程，等那条流程实现后
// 再单独加对应的场景，不要混进常规扫描用例。
//
// 随机部分用固定种子，dev:seed 每次生成的命名完全一致，测试才可复现；
// 已存在的文件不会被覆盖，因此可以反复执行。
const (
	seedClipSeconds = 3
	seedVideoSize   = "320x180"
	seedFrameRate   = 12
	// seedRandomSeed 固定随机种子：命名要有变化，但不能每次跑都不一样。
	seedRandomSeed = 20261013
)

// 生成器的词表。中文名与英文名分开，命名时按结构决定是否拼接。
var (
	seedTitles = []struct{ cn, kr, en string }{
		{cn: "流浪地球", en: "The.Wandering.Earth"},
		{cn: "满江红", en: "Full.The.Story.River"},
		{cn: "无问西东", en: "Forever.Young"},
		{cn: "云朵上的民族", en: "Nation.on.Clouds"},
		{cn: "出走的决心", en: "Like.a.Rolling.Stone"},
		{kr: "기생충", en: "Parasite"},
		{kr: "올드보이", en: "Oldboy"},
		{en: "Arrival"},
		{en: "Sic.parvis.magna"},
		{en: "Stalker"},
		{en: "Ballad.of.Filibuster"},
		{en: "The.Wandering.Earth"},
		{en: "Perfect.Days"},
	}
	seedTVTitles = []struct{ cn, kr, en string }{
		{cn: "锵锵行天下", en: "World.Tour.with.Wen.Tao"},
		{cn: "走向共和", en: "Towards.Republic"},
		{cn: "沉默的真相", en: "The.Silent.Truth"},
		{kr: "오리지널", en: "Original"},
		{en: "Adolescence"},
		{en: "Steins.Gate"},
		{en: "Yellowstone"},
		{en: "The.Handmaids.Tale"},
		{en: "Boardwalk.Empire"},
		{en: "Silo"},
		{en: "Severance"},
	}
	seedSources  = []string{"WEB-DL", "WEBRip", "BluRay", "BDRip", "HDTV", "REMUX", "Blu-ray"}
	seedReses    = []string{"720p", "1080p", "1080i", "2160p"}
	seedCodecs   = []string{"H264", "H265", "x264", "x265", "AVC", "HEVC"}
	seedAudios   = []string{"AAC", "AAC2.0", "FLAC", "DTS-HD.MA5.1", "TrueHD.7.1", "DDP5.1"}
	seedGroups   = []string{"ADE", "ADWeb", "OurTV", "HDSWEB", "kmnn111@Sunny", "7³ACG@OurBits", "CiNEPHiLES", "TJUPT", "bulgur", "FEVER"}
	seedYears    = []string{"2007", "2012", "2016", "2019", "2021", "2023", "2024"}
	seedMovieExt = []string{".mkv", ".mkv", ".mp4", ".ts"}
	seedTVExt    = []string{".mkv", ".mkv", ".mp4"}
)

// seedClip 是一个待生成的媒体文件。
type seedClip struct {
	// rel 相对 tests/media 的路径。
	rel string
	// hue 让每个文件画面可区分，便于肉眼确认播放的是哪一个。
	hue int
}

// seedNameGen 按固定种子产出命名组合。
type seedNameGen struct {
	rnd *rand.Rand
}

func newSeedNameGen() *seedNameGen {
	return &seedNameGen{rnd: rand.New(rand.NewSource(seedRandomSeed))}
}

func (g *seedNameGen) pick(list []string) string {
	return list[g.rnd.Intn(len(list))]
}

func (g *seedNameGen) year() string { return g.pick(seedYears) }

// tag 拼出「来源.分辨率.编码.音频-制作组」这一段。
func (g *seedNameGen) tag() string {
	return fmt.Sprintf("%s.%s.%s.%s-%s",
		g.pick(seedSources), g.pick(seedReses), g.pick(seedCodecs), g.pick(seedAudios), g.pick(seedGroups))
}

func (g *seedNameGen) ext(list []string) string { return g.pick(list) }

// leadingName 返回标题前缀：中文名用方括号包住再跟英文名，韩文名直接跟英文名。
// 两者都没有就只用英文名。
func (g *seedNameGen) leadingName(title struct{ cn, kr, en string }, bracket bool) string {
	switch {
	case title.cn != "":
		if bracket {
			return "[" + title.cn + "]." + title.en
		}
		return title.cn + "." + title.en
	case title.kr != "":
		return title.kr + "." + title.en
	default:
		return title.en
	}
}

// buildClips 生成完整的文件清单。结构上刻意覆盖真实网盘里常见的几种摆法。
func buildClips() []seedClip {
	g := newSeedNameGen()
	var clips []seedClip
	hue := 0
	add := func(rel string) {
		clips = append(clips, seedClip{rel: rel, hue: hue % 360})
		hue += 47
	}

	// 电影：单目录 + 同名文件，目录名与文件名一致。
	for i := 0; i < 3; i++ {
		title := g.rnd.Intn(len(seedTitles))
		name := g.leadingName(seedTitles[title], i%2 == 0) + "." + g.year() + "." + g.tag()
		add("电影/" + name + "/" + name + g.ext(seedMovieExt))
	}

	// 电影：裸文件，直接躺在 movie 目录里，没有子目录。
	for i := 0; i < 2; i++ {
		title := g.rnd.Intn(len(seedTitles))
		name := g.leadingName(seedTitles[title], false) + "." + g.year() + "." + g.tag()
		add("电影/" + name + g.ext(seedMovieExt))
	}

	// 电影：中文名 + 英文名直接拼在文件名上，不带方括号。
	for i := 0; i < 2; i++ {
		title := seedTitles[g.rnd.Intn(len(seedTitles))]
		if title.cn == "" {
			title = seedTitles[0]
		}
		name := title.cn + " " + title.en + " " + g.year() + "." + g.tag()
		add("电影/" + name + g.ext(seedMovieExt))
	}

	// 剧集：目录名带季号，文件名是 S01Exx。
	for i := 0; i < 3; i++ {
		title := seedTVTitles[g.rnd.Intn(len(seedTVTitles))]
		base := title.en + ".S01." + g.year() + "." + g.tag()
		ep := fmt.Sprintf("%s.S01E%02d.%s.%s", title.en, i+1, g.year(), g.tag())
		add("剧集/" + base + "/" + ep + g.ext(seedTVExt))
	}

	// 剧集：剧集 / 季目录 / 文件三层，文件名用 S01EPxx。
	for i := 0; i < 2; i++ {
		title := seedTVTitles[g.rnd.Intn(len(seedTVTitles))]
		season := title.en + ".S0" + strconv.Itoa(i+1) + "." + g.year() + "." + g.tag()
		ep := fmt.Sprintf("%s.S0%dEP%02d.%s.%s", title.en, i+1, i+1, g.year(), g.tag())
		add("剧集/" + title.en + "/" + season + "/" + ep + g.ext(seedTVExt))
	}

	// 剧集：目录名不带季号，用「第N季 + 年份」表示，文件名是 E01 / 带日期。
	for i := 0; i < 3; i++ {
		title := seedTVTitles[g.rnd.Intn(len(seedTVTitles))]
		name := title.cn
		if name == "" {
			name = title.en
		}
		dir := fmt.Sprintf("%s 第%d季%s.-%s.%s.Complete.%s", name, i%2+1, g.year(), title.en, g.year(), g.pick(seedReses))
		ep := fmt.Sprintf("%s.E%02d.%s%s.%s", title.en, i+1, g.year(),
			g.pick([]string{"0101", "0315", "0708", "1120"}), g.tag())
		add("剧集/" + dir + "/" + ep + g.ext(seedTVExt))
	}

	// 剧集：目录名不带季号也不含「第N季」，文件名用 EP01。
	for i := 0; i < 2; i++ {
		title := seedTVTitles[g.rnd.Intn(len(seedTVTitles))]
		dir := title.en + "." + g.year() + "." + g.tag()
		ep := fmt.Sprintf("%s.EP%02d.%s.%s", title.en, i+1, g.year(), g.tag())
		add("剧集/" + dir + "/" + ep + g.ext(seedTVExt))
	}

	// 剧集：目录名带空格与全角括号，文件名是纯中文加序号。
	for i := 0; i < 2; i++ {
		title := seedTVTitles[g.rnd.Intn(len(seedTVTitles))]
		name := title.cn
		if name == "" {
			name = title.en
		}
		dir := name + " MP4（H265编码）"
		add("剧集/" + dir + "/" + name + fmt.Sprintf("%02d", i+1) + ".mp4")
	}

	// 合集：一个目录放多部，模拟 PT 合集的打包方式。
	collection := fmt.Sprintf("合集/%s.Collections.1979-2017.%s", seedTitles[0].en, g.tag())
	for i := 0; i < 3; i++ {
		title := seedTitles[g.rnd.Intn(len(seedTitles))]
		add(collection + "/" + title.en + "." + g.year() + "." + g.tag() + g.ext(seedMovieExt))
	}

	return clips
}

// runFFmpeg 生成一个几秒的测试视频：纯色画面 + 静音 AAC 音轨。
// 文件很小但能被 ffprobe 正常识别，读媒体信息与播放测试都能用。
func runFFmpeg(full, ext string, hue int, seconds int) error {
	cmd := exec.Command("ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%06x:s=%s:d=%d:r=%d", hue, seedVideoSize, seconds, seedFrameRate),
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo",
		"-t", strconv.Itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "40", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "48k", "-ac", "2",
		"-shortest",
	)
	// .ts / .mp4 用对应容器，其余按扩展名交给 ffmpeg 推断。
	switch strings.ToLower(filepath.Ext(ext)) {
	case ".ts":
		cmd.Args = append(cmd.Args, "-f", "mpegts")
	case ".mp4":
		cmd.Args = append(cmd.Args, "-movflags", "+faststart")
	}
	cmd.Args = append(cmd.Args, full)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg 生成 %s 失败: %w\n%s", filepath.Base(full), err, out)
	}
	return nil
}

// seedMediaFiles 按清单生成媒体文件，已存在的保持原样。
func seedMediaFiles(mediaDir string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("找不到 ffmpeg：媒体文件由 ffmpeg 生成，请先安装（%w）", err)
	}
	clips := buildClips()
	generated, skipped := 0, 0
	for _, clip := range clips {
		full := filepath.Join(mediaDir, filepath.FromSlash(clip.rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("创建媒体目录: %w", err)
		}
		if _, err := os.Stat(full); err == nil {
			skipped++
		} else if !os.IsNotExist(err) {
			return err
		} else {
			if err := runFFmpeg(full, filepath.Ext(full), clip.hue, seedClipSeconds); err != nil {
				return err
			}
			generated++
		}
	}
	fmt.Printf("  媒体文件  新建 %d 个，已存在 %d 个，共 %d 个\n", generated, skipped, len(clips))
	return nil
}
