//---------------------------------------------------------------------------------------------------
// Calling SNPs based on read-multigenome alignment.
// Copyright 2014 Nam Sy Vo
//---------------------------------------------------------------------------------------------------

package isc

import (
	"fmt"
	"bufio"
	"math"
	"math/rand"
	"os"
	"strconv"
	"time"
	"sort"
	"sync"
)

//--------------------------------------------------------------------------------------------------
// Global variables for alignment and SNP calling process.
//--------------------------------------------------------------------------------------------------
var (
	INPUT_INFO	InputInfo	//Input information
	PARA_INFO	ParaInfo	//Parameters information
	RAND_GEN	*rand.Rand 	//Pseudo-random number generator
	INDEX		Index      	//Index for alignment
)

type Debug_info struct {
	read1, read2 []byte
	left_most_pos1, left_most_pos2 int
	snp_num1, snp_num2 int
	snp_diff_dis1, snp_diff_dis2 []int
}

//--------------------------------------------------------------------------------------------------
// SNP represents SNP obtained during alignment phase.
// It serves as temporary variable during SNP calling phase.
//--------------------------------------------------------------------------------------------------
type SNP struct {
	Pos 	uint32 //SNP postion on ref
	Bases 	[]byte //bases of SNP
	BaseQ 	[]byte //quality of bases of SNP
}

//--------------------------------------------------------------------------------------------------
// SNP_Prof represents all possible SNPs and their probablilties at all positions on reference multigenome.
// This struct also has functions defined on it for calling SNPs.
// SNP_Calls stores all possible variants at each position and their probablilities of being SNP calls.
// Their initial (prior) probablities will be obtained from reference genomes and SNP profiles.
// Their posterior probabilities will be updated during alignment phase based on information from read-multigenome alignment
type SNP_Prof struct {
	SNP_Calls 	map[uint32]map[string]float64
}

//--------------------------------------------------------------------------------------------------
// InitIndex initializes indexes and parameters.
// This function will be called from main program.
//--------------------------------------------------------------------------------------------------
func (S *SNP_Prof) Init(input_info InputInfo) {

	INPUT_INFO = input_info
	PARA_INFO = *SetPara(100, 0.001)
	INDEX.Init()

	// Assign all possible SNPs and their prior probabilities from SNP profile.
	S.SNP_Calls = make(map[uint32]map[string]float64)
	
	RAND_GEN = rand.New(rand.NewSource(time.Now().UnixNano()))
}

//--------------------------------------------------------------------------------------------------
// CallSNPs initializes share variables, channels, reads input reads, finds all possible SNPs,
// and updates SNP information in SNP_Prof.
// This function will be called from main program.
//--------------------------------------------------------------------------------------------------
func (S *SNP_Prof) CallSNPs() (int, int) {

	//The channel read_signal is used for signaling between goroutines which run ReadReads and FindSNPs,
	//when a FindSNPs goroutine finish copying a read to its own memory, it signals ReadReads goroutine to scan next reads.
	read_signal := make(chan bool)

	//Call a goroutine to read input reads
	read_data := make(chan ReadInfo, INPUT_INFO.Routine_num)
	go S.ReadReads(read_data, read_signal)

	//Call goroutines to find SNPs, pass shared variable to each goroutine
	snp_results := make(chan SNP)
	debug_info := make(chan Debug_info)
	debug_reads := make(chan []byte)
	var wg sync.WaitGroup
	for i := 0; i < INPUT_INFO.Routine_num; i++ {
		go S.FindSNPs(read_data, read_signal, snp_results, &wg, debug_info, debug_reads)
	}
	go func() {
		wg.Wait()
		close(snp_results)
		close(debug_info)
		close(debug_reads)
	}()

	//Collect SNPs from results channel and update SNPs and their probabilities
	go func() {
		var snp SNP
		for snp = range snp_results {
			if len(snp.Bases) == 1 {
				S.UpdateSNPProb(snp)
			} else {
				S.UpdateIndelProb(snp)
			}
		}
	}()
	go func() {
		for d := range debug_info {
			//fmt.Println(string(d.read1), string(d.read2), d.left_most_pos1, d.left_most_pos2, d.snp_num1, d.snp_num2)
			fmt.Println("read1, snp\t", string(d.read1))
		}
	}()
	for r := range debug_reads {
		//fmt.Println(string(d.read1), string(d.read2), d.left_most_pos1, d.left_most_pos2, d.snp_num1, d.snp_num2)
		fmt.Println("read1, input\t", string(r))
	}
	return 0, 0
}

//--------------------------------------------------------------------------------------------------
// ReadReads reads all reads from input FASTQ files and put them into data channel.
//--------------------------------------------------------------------------------------------------
func (S *SNP_Prof) ReadReads(read_data chan ReadInfo, read_signal chan bool) {

	fn1, fn2 := INPUT_INFO.Read_file_1, INPUT_INFO.Read_file_2
	f1, err_f1 := os.Open(fn1)
	if err_f1 != nil {
		panic("Error opening input read file " + fn1)
	}
	defer f1.Close()
	f2, err_f2 := os.Open(fn2)
	if err_f2 != nil {
		panic("Error opening input read file " + fn2)
	}
	defer f2.Close()

	read_num := 0
	scanner1 := bufio.NewScanner(f1)
	scanner2 := bufio.NewScanner(f2)
	var read_info ReadInfo
	read_info.Read1 = make([]byte, 200)
	read_info.Read2 = make([]byte, 200)
	read_info.Qual1 = make([]byte, 200)
	read_info.Qual1 = make([]byte, 200)

	for scanner1.Scan() && scanner2.Scan() { //ignore 1st lines in input FASTQ files
		scanner1.Scan()
		scanner2.Scan()
		copy(read_info.Read1, scanner1.Bytes()) //use 2nd line in input FASTQ file 1
		copy(read_info.Read2, scanner2.Bytes()) //use 2nd line in input FASTQ file 1
		scanner1.Scan() //ignore 3rd line in 1st input FASTQ file 1
		scanner2.Scan() //ignore 3rd line in 2nd input FASTQ file 2
		scanner1.Scan()
		scanner2.Scan()
		copy(read_info.Qual1, scanner1.Bytes()) //use 4th line in input FASTQ file 1
		copy(read_info.Qual2, scanner2.Bytes()) //use 4th line in input FASTQ file 2
		if len(read_info.Read1) > 0 && len(read_info.Read2) > 0 {
			read_num++
			read_data <- read_info
			//PrintMemStats("After putting read to data " + string(read_info.Read1))
			read_signal <- true
		}
		//if read_num%10000 == 0 {
		//	PrintMemStats("Memstats after distributing 10000 reads")
		//}
	}
	close(read_data)
}

//--------------------------------------------------------------------------------------------------
// FindSNPs takes data from data channel, find all possible SNPs and put them into results channel.
//--------------------------------------------------------------------------------------------------
func (S *SNP_Prof) FindSNPs(read_data chan ReadInfo, read_signal chan bool, snp_results chan SNP, 
	wg *sync.WaitGroup, debug_info chan Debug_info, debug_reads chan []byte) {

	//Initialize inter-function share variables
	read_info := InitReadInfo(PARA_INFO.Read_len)
	align_info := InitAlignInfo(PARA_INFO.Read_len)
	match_pos := make([]int, PARA_INFO.Max_match)

	wg.Add(1)
	defer wg.Done()
	var read ReadInfo
	for read = range read_data {
		read_test := make([]byte, len(read.Read1))
		copy(read_test, read.Read1)
		debug_reads <- read_test
		//PrintMemStats("Before copying all info from data chan")
		copy(read_info.Read1, read.Read1)
		copy(read_info.Read2, read.Read2)
		copy(read_info.Qual1, read.Qual1)
		copy(read_info.Qual2, read.Qual2)
		<- read_signal
		//PrintMemStats("After copying all info from data chan")
		RevComp(read_info.Read1, read_info.Rev_read1, read_info.Rev_comp_read1, read_info.Comp_read1)
		//PrintMemStats("After calculating RevComp for Read1")
		RevComp(read_info.Read2, read_info.Rev_read2, read_info.Rev_comp_read2, read_info.Comp_read2)
		//PrintMemStats("After calculating RevComp for Read2")
		S.FindSNPsFromReads(read_info, snp_results, align_info, match_pos, debug_info)
		//PrintMemStats("After finding all SNPs from reads")
	}
}

//--------------------------------------------------------------------------------------------------
// FindSNPsFromReads returns SNPs found from alignment between pair-end reads and the multigenome.
// This version treats each end of the reads independently.
//--------------------------------------------------------------------------------------------------
func (S *SNP_Prof) FindSNPsFromReads(read_info *ReadInfo, snp_results chan SNP, align_info *AlignInfo, match_pos []int, debug_info chan Debug_info) {

	var snps1, snps2 []SNP
	var left_most_pos1, left_most_pos2 int
	//Find SNPs for the first end
	//PrintMemStats("Before FindSNPsFromEnd1")
	snps1, left_most_pos1 = S.FindSNPsFromEachEnd(read_info.Read1, read_info.Rev_read1, read_info.Rev_comp_read1, 
		read_info.Comp_read1, read_info.Qual1, align_info, match_pos)
	//PrintMemStats("After FindSNPsFromEnd1")

	//Find SNPs for the second end
	//PrintMemStats("Before FindSNPsFromEnd2")
	snps2, left_most_pos2 = S.FindSNPsFromEachEnd(read_info.Read2, read_info.Rev_read2, read_info.Rev_comp_read2, 
		read_info.Comp_read2, read_info.Qual2, align_info, match_pos)
	//PrintMemStats("After FindSNPsFromEnd2")

	//Will process constrants of two ends here
	//...
	var d Debug_info
	d.read1 = read_info.Read1
	d.read2 = read_info.Read2
	d.left_most_pos1 = left_most_pos1
	d.left_most_pos2 = left_most_pos2
	d.snp_num1 = len(snps1)
	d.snp_num2 = len(snps2)
	debug_info <- d

	var snp SNP
	if len(snps1) > 0 {
		for _, snp = range snps1 {
			snp_results <- snp
		}
	}
	if len(snps2) > 0 {
		for _, snp = range snps2 {
			snp_results <- snp
		}
	}
}

//---------------------------------------------------------------------------------------------------
// FindSNPsFromEachEnd find SNPs from alignment between read (one end) and multigenome.
//---------------------------------------------------------------------------------------------------
func (S *SNP_Prof) FindSNPsFromEachEnd(read, rev_read, rev_comp_read, comp_read, qual []byte, 
	align_info *AlignInfo, match_pos []int) ([]SNP, int) {
	var has_seeds bool
	var p, s_pos, e_pos int
	var loop_num, match_num int
	var snps []SNP

	p = INPUT_INFO.Start_pos
	loop_num = 1
	var left_most_pos int
	for loop_num <= PARA_INFO.Iter_num {
		//fmt.Println(loop_num, "\tread2")
		//PrintMemStats("Before FindSeeds, original_read, loop_num " + strconv.Itoa(loop_num))
		s_pos, e_pos, match_num, has_seeds = INDEX.FindSeeds(read, rev_read, p, match_pos)
		//PrintMemStats("After FindSeeds, original_read, loop_num " + strconv.Itoa(loop_num))
		if has_seeds {
			//fmt.Println("read2, has seed\t", s_pos, "\t", e_pos, "\t", string(read_info.Read2))
			//PrintMemStats("Before FindSNPsFromMatch, original_read, loop_num " + strconv.Itoa(loop_num))
			snps, left_most_pos = S.FindSNPsFromMatch(read, qual, s_pos, e_pos, match_pos, match_num, align_info)
			//PrintMemStats("After FindSeeds, original_read, loop_num " + strconv.Itoa(loop_num))
			if len(snps) > 0 {
				//fmt.Println("read2, has snp\t", s_pos, "\t", e_pos, "\t", string(read_info.Read2))
				return snps, left_most_pos
			}
		}
		//Find SNPs for the reverse complement of the second end
		//PrintMemStats("Before FindSeeds, revcomp_read, loop_num " + strconv.Itoa(loop_num))
		s_pos, e_pos, match_num, has_seeds = INDEX.FindSeeds(rev_comp_read, comp_read, p, match_pos)
		//PrintMemStats("After FindSeeds, revcomp_read, loop_num " + strconv.Itoa(loop_num))
		if has_seeds {
			//fmt.Println("rc_read2, has seed\t", s_pos, "\t", e_pos, "\t", string(read_info.Rev_comp_read2))
			//PrintMemStats("Before FindSNPsFromMatch, revcomp_read, loop_num " + strconv.Itoa(loop_num))
			snps, left_most_pos = S.FindSNPsFromMatch(rev_comp_read, qual, s_pos, e_pos, match_pos, match_num, align_info)
			//PrintMemStats("After FindSNPsFromMatch, revcomp_read, loop_num " + strconv.Itoa(loop_num))
			if len(snps) > 0 {
				//fmt.Println("rc_read2, has snp\t", s_pos, "\t", e_pos, "\t", string(read_info.Rev_comp_read2))
				return snps, left_most_pos
			}
		}
		//Take a new position to search
		if INPUT_INFO.Search_mode == 1 {
			p = RAND_GEN.Intn(len(read) - 1) + 1
		} else if INPUT_INFO.Search_mode == 2 {
			p = p + INPUT_INFO.Search_step
		}
		loop_num++
	}
	return snps, left_most_pos
}

//---------------------------------------------------------------------------------------------------
// FindSNPsFromMatch finds SNPs from extensions of matches between read (one end) and multigenome.
//---------------------------------------------------------------------------------------------------
func (S *SNP_Prof) FindSNPsFromMatch(read, qual []byte, s_pos, e_pos int, 
	match_pos []int, match_num int, align_info *AlignInfo) ([]SNP, int) {

	var pos, k, dis int
	var left_snp_pos, right_snp_pos, left_snp_idx, right_snp_idx []int
	var left_snp_val, right_snp_val [][]byte
	var snps []SNP
	var snp SNP

	min_dis := INF
	var left_most_pos, min_pos int
	for i := 0; i < match_num; i++ {
		pos = match_pos[i]
		//PrintMemStats("Before FindExtensions, match_num " + strconv.Itoa(i))
		dis, left_snp_pos, left_snp_val, left_snp_idx, right_snp_pos, right_snp_val, right_snp_idx, left_most_pos =
			 INDEX.FindExtensions(read, s_pos, e_pos, pos, align_info)
		//PrintMemStats("After FindExtensions, match_num " + strconv.Itoa(i))
		if dis <= PARA_INFO.Dist_thres {
			if len(left_snp_pos) != 0 || len(right_snp_pos) != 0 {
				if min_dis > dis {
					min_dis = dis
					min_pos = left_most_pos
					snps = make([]SNP, 0)
					for k = 0; k < len(left_snp_pos); k++ {
						//PrintMemStats("Before GetSNP left, snp_num " + strconv.Itoa(k))
						left_snp_qual := make([]byte, len(left_snp_val[k]))
						copy(left_snp_qual, qual[left_snp_idx[k] : left_snp_idx[k] + len(left_snp_val[k])])
						snp.Pos, snp.Bases, snp.BaseQ = uint32(left_snp_pos[k]), left_snp_val[k], left_snp_qual
						snps = append(snps, snp)
						//PrintMemStats("After GetSNP left, snp_num " + strconv.Itoa(k))
					}
					for k = 0; k < len(right_snp_pos); k++ {
						//PrintMemStats("Before GetSNP right, snp_num " + strconv.Itoa(k))
						right_snp_qual := make([]byte, len(right_snp_val[k]))
						copy(right_snp_qual, qual[right_snp_idx[k] : right_snp_idx[k] + len(right_snp_val[k])])
						snp.Pos, snp.Bases, snp.BaseQ = uint32(right_snp_pos[k]), right_snp_val[k], right_snp_qual
						snps = append(snps, snp)
						//PrintMemStats("After GetSNP right, snp_num " + strconv.Itoa(k))
					}
				}
			}
		}
	}
	return snps, min_pos
}

//---------------------------------------------------------------------------------------------------
// UpdateSNPProb updates SNP probablilities for all possible SNPs.
// Input: a snp of type SNP.
// Output: updated S.SNP_Calls[snp.Pos] based on snp.Bases and snp.BaseQ using Bayesian method.
//---------------------------------------------------------------------------------------------------
func (S *SNP_Prof) UpdateSNPProb(snp SNP) {
	pos := snp.Pos
	a := string(snp.Bases)
	q := snp.BaseQ[0]

	var p float64
	p_ab := make(map[string]float64)
	p_a := 0.0

	if _, snp_call_exist := S.SNP_Calls[pos]; !snp_call_exist {
		S.SNP_Calls[pos] = make(map[string]float64)
		if snps, snp_prof_exist := INDEX.SNP_PROF[int(pos)]; snp_prof_exist {
			snp_prof_num := len(snps)
			for idx, snp := range snps {
				S.SNP_Calls[pos][string(snp)] = float64(INDEX.SNP_AF[int(pos)][idx]) - float64(snp_prof_num) * EPSILON
			}
		} else {
			S.SNP_Calls[pos][string(INDEX.SEQ[int(pos)])] = 1 - 3 * EPSILON
		}
		for _, b := range STD_BASES {
			if _, ok := S.SNP_Calls[pos][string(b)]; !ok {
				S.SNP_Calls[pos][string(b)] = EPSILON
			}
		}
	}

	for b, p_b := range(S.SNP_Calls[pos]) {
		if a == b {
			p = 1.0 - math.Pow(10, -(float64(q) - 33) / 10.0) //Phred-encoding factor (33) need to be estimated from input data
		} else {
			p = math.Pow(10, -(float64(q) - 33) / 10.0) / 3 //need to be refined, e.g., checked with diff cases (snp vs. indel)
		}
		p_ab[b] = p
		p_a += p_b * p_ab[b]
	}
	for b, p_b := range(S.SNP_Calls[pos]) {
		S.SNP_Calls[pos][b] = p_b * (p_ab[b] / p_a)
	}
}

//---------------------------------------------------------------------------------------------------
// UpdateIndelProb updates Indel probablilities for all possible Indels.
// Input: a snp of type SNP.
// Output: updated S.SNP_Calls[snp.Pos] based on snp.Bases and snp.BaseQ using Bayesian method.
//---------------------------------------------------------------------------------------------------
func (S *SNP_Prof) UpdateIndelProb(snp SNP) {
	pos := snp.Pos
	a := string(snp.Bases)
	q := snp.BaseQ
	if len(a) == 0 {
		a = "."
		q = []byte{'I'} //need to be changed to a proper value
	}

	var p float64
	var qi byte
	p_ab := make(map[string]float64)
	p_a := 0.0

	if _, snp_call_exist := S.SNP_Calls[pos]; !snp_call_exist {
		S.SNP_Calls[pos] = make(map[string]float64)
		if snps, snp_prof_exist := INDEX.SNP_PROF[int(pos)]; snp_prof_exist {
			snp_prof_num := len(snps)
			for idx, snp := range snps {
				S.SNP_Calls[pos][string(snp)] = float64(INDEX.SNP_AF[int(pos)][idx]) - float64(snp_prof_num) * EPSILON
			}
		} else {
			S.SNP_Calls[pos][string(INDEX.SEQ[int(pos)])] = 1 - 3 * EPSILON
		}
		for _, b := range STD_BASES {
			if _, ok := S.SNP_Calls[pos][string(b)]; !ok {
				S.SNP_Calls[pos][string(b)] = EPSILON
			}
		}
	}

	if _, ok := S.SNP_Calls[pos][a]; !ok {
		S.SNP_Calls[pos][a] = EPSILON
	}

	for b, p_b := range(S.SNP_Calls[pos]) {
		p = 1
		if a == b {
			for _, qi = range q {
				p *= (1.0 - math.Pow(10, -(float64(qi) - 33) / 10.0)) //Phred-encoding factor (33) need to be estimated from input data
			}
		} else {
			for _, qi = range q {
				p *= (math.Pow(10, -(float64(qi) - 33) / 10.0) / 3) //need to be refined, e.g., checked with diff cases (snp vs. indel)
			}
		}
		p_ab[b] = p
		p_a += p_b * p_ab[b]
	}
	for b, p_b := range(S.SNP_Calls[pos]) {
		S.SNP_Calls[pos][b] = p_b * (p_ab[b] / p_a)
	}
}

//-------------------------------------------------------------------------------------------------------
// OutputSNPCalls determines SNP calls, convert their probabilities to Phred scores, and writes them to file
// in proper format (VCF-like format in this version).
//-------------------------------------------------------------------------------------------------------
func (S *SNP_Prof) OutputSNPCalls() {

	file, err := os.Create(INPUT_INFO.SNP_call_file)
	if err != nil {
		return
	}
	defer file.Close()

	var snp_pos uint32
	var str_snp_pos, snp_qual string

	SNP_Pos := make([]int, 0, len(S.SNP_Calls))
	for snp_pos, _ = range S.SNP_Calls {
		SNP_Pos = append(SNP_Pos, int(snp_pos))
	}
	sort.Ints(SNP_Pos)

	var snp_call_prob, snp_prob float64
	var snp_call, snp string
	for _, pos := range SNP_Pos {
		snp_pos = uint32(pos)
		str_snp_pos = strconv.Itoa(pos)
		snp_call_prob = 0
		for snp, snp_prob = range S.SNP_Calls[snp_pos] {
			if snp_call_prob < snp_prob {
				snp_call_prob = snp_prob
				snp_call = snp
			}
		}
		snp_qual = strconv.FormatFloat(-10 * math.Log10(1 - snp_call_prob), 'f', 5, 32)
		if snp_qual != "+Inf" {
			_, err = file.WriteString(str_snp_pos + "\t" + snp_call + "\t" + snp_qual + "\n")
		} else {
			_, err = file.WriteString(str_snp_pos + "\t" + snp_call + "\t1000\n")
		}
		if err != nil {
			fmt.Println(err)
			break
		}
	}
}