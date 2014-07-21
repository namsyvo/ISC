//--------------------------------------------------------------------------------------------------
// Aligning reads to multigenomes by extending exact matches based on read-multigenome edit distance.
// Finding exact matches betwwen reads and multigenomes using exact search with FM index.
// Exact search is perfomed with regard to a random position on reads.
// Finding inexact matches betwwen reads and multigenomes by extending FM-index based exact matches
// using edit distance between reads and multigenomes.
// Determining whether an interval on multigenomes contains a SNP position using interpolation search.
// Copyright 2014 Nam Sy Vo
//--------------------------------------------------------------------------------------------------


package isc

import (
    "fmt"
    "math"
    "sort"
	"runtime"
	"log"
    "github.com/vtphan/fmi" //to use FM index
)

var (
    DIST_THRES int = INF //threshold for distances between reads and multigenomes
    ITER_NUM int = INF //number of random iterations to find proper seeds
    MAXIMUM_MATCH int = INF //maximum number of matches
)

//--------------------------------------------------------------------------------------------------
// Init function sets initial values for global variables and parameters for Index object
//--------------------------------------------------------------------------------------------------
func (I *Index) Init(input_info InputInfo, read_info ReadInfo, para_info ParaInfo) {

    memstats := new(runtime.MemStats)

    I.SEQ = LoadMultigenome(input_info.Genome_file)
	runtime.ReadMemStats(memstats)
    log.Printf("align.go: memstats after loading multigenome:\t%d\t%d\t%d\t%d\t%d", memstats.Alloc, memstats.TotalAlloc, memstats.Sys, memstats.HeapAlloc, memstats.HeapSys)

    I.SNP_PROF, I.SNP_AF, I.SAME_LEN_SNP = LoadSNPLocation(input_info.SNP_file)
	runtime.ReadMemStats(memstats)
    log.Printf("align.go: memstats after loading SNP profile:\t%d\t%d\t%d\t%d\t%d", memstats.Alloc, memstats.TotalAlloc, memstats.Sys, memstats.HeapAlloc, memstats.HeapSys)

    I.SORTED_SNP_POS = make([]int, 0, len(I.SNP_PROF))
    for k := range I.SNP_PROF {
        I.SORTED_SNP_POS = append(I.SORTED_SNP_POS, k)
    }
    sort.Sort(sort.IntSlice(I.SORTED_SNP_POS))
	runtime.ReadMemStats(memstats)
    log.Printf("align.go: memstats after loading sorted SNP postions:\t%d\t%d\t%d\t%d\t%d", memstats.Alloc, memstats.TotalAlloc, memstats.Sys, memstats.HeapAlloc, memstats.HeapSys)

    I.REV_FMI = *fmi.Load(input_info.Rev_index_file)
	runtime.ReadMemStats(memstats)
    log.Printf("align.go: memstats after loading index of reverse multigenome:\t%d\t%d\t%d\t%d\t%d", memstats.Alloc, memstats.TotalAlloc,
        memstats.Sys, memstats.HeapAlloc, memstats.HeapSys)

    //Const for computing distance
	err := float64(read_info.Seq_err)
	rlen := float64(read_info.Read_len)
	k := float64(para_info.Err_var_factor)
    DIST_THRES = int(math.Ceil(err * rlen + k * math.Sqrt(rlen * err * (1 - err))))
    ITER_NUM = para_info.Iter_num_factor * (DIST_THRES + 1)
    MAXIMUM_MATCH = para_info.Max_match

    fmt.Println("DIST_THRES: ", DIST_THRES)
    fmt.Println("ITER_NUM: ", ITER_NUM)
    fmt.Println("MAXIMUM_MATCH: ", MAXIMUM_MATCH)
}

//--------------------------------------------------------------------------------------------------
// Bachward Search with FM-index, start from any position on the pattern.
//--------------------------------------------------------------------------------------------------
func (I *Index) BackwardSearchFrom(index fmi.Index, pattern []byte, start_pos int) (int, int, int){
	var sp, ep, offset int
	var ok bool

	c := pattern[start_pos]
	sp, ok = index.C[c]
	if !ok {
		return 0, -1, -1
	}
	ep = index.EP[c]
	var sp0, ep0 int
	// if Debug { fmt.Println("pattern: ", string(pattern), "\n\t", string(c), sp, ep) }
	for i:= start_pos - 1 ; i >= 0 ; i-- {
		//fmt.Println("pos, # candidates: ", i, ep - sp + 1)
  		c = pattern[i]
  		offset, ok = index.C[c]
  		if ok {
			sp0 = offset + index.OCC[c][sp - 1]
			ep0 = offset + index.OCC[c][ep] - 1
			if sp0 <= ep0 {
				sp = sp0
				ep = ep0
			} else {
				return sp, ep, i + 1
			}
		} else {
			return  0, -1, -1
		}
  		// if Debug { fmt.Println("\t", string(c), sp, ep) }
	}
	return sp, ep, 0
}

//--------------------------------------------------------------------------------------------------
// FindSeeds function returns positions and distances of LCS between reads and multi-genomes.
// It uses both backward search and forward search (backward search on reverse references).
//--------------------------------------------------------------------------------------------------
func (I *Index) FindSeeds(read, rev_read []byte, p int, match_pos []int) (int, int, int, bool) {

    var rev_sp, rev_ep int = 0, MAXIMUM_MATCH
    var rev_s_pos, rev_e_pos, s_pos, e_pos int
	
    rev_s_pos = READ_LEN - 1 - p
    rev_sp, rev_ep, rev_e_pos = I.BackwardSearchFrom(I.REV_FMI, rev_read, rev_s_pos)
	if rev_e_pos >= 0 {
		var idx int
		//convert rev_e_pos in forward search to s_pos in backward search
		s_pos = READ_LEN - 1 - rev_e_pos
		e_pos = p
		if rev_ep - rev_sp + 1 <= MAXIMUM_MATCH {
		    for idx = rev_sp ; idx <= rev_ep ; idx++ {
		        match_pos[idx - rev_sp] = len(I.SEQ) - 1 - I.REV_FMI.SA[idx] - (s_pos - e_pos)
		    }
		    return s_pos, e_pos, rev_ep - rev_sp + 1, true
		}
	    return s_pos, e_pos, rev_ep - rev_sp + 1, false
	}
    return -1, -1, -1, false // will be changed later
}

//-----------------------------------------------------------------------------------------------------
// FindExtension function returns alignment (snp report) between between reads and multi-genomes.
// The alignment is built within a given threshold of distance.
//-----------------------------------------------------------------------------------------------------
func (I *Index) FindExtensions(read []byte, s_pos, e_pos int, match_pos int, align_mem AlignMem) (int, int, int, bool) {

    var ref_left_flank, ref_right_flank, read_left_flank, read_right_flank []byte
    var lcs_len int = s_pos - e_pos + 1

    var isSNP, isSameLenSNP bool
    left_ext_add_len, right_ext_add_len := 0, 0
    i := 0
    for i = match_pos - e_pos; i < match_pos; i++ {
        _, isSNP = I.SNP_PROF[i]
        _, isSameLenSNP = I.SAME_LEN_SNP[i]
        if isSNP && !isSameLenSNP {
            left_ext_add_len++
        }
    }
    for i = match_pos + lcs_len; i < (match_pos + lcs_len) + (len(read) - s_pos) - 1; i++ {
        _, isSNP = I.SNP_PROF[i]
        _, isSameLenSNP = I.SAME_LEN_SNP[i]
        if isSNP && !isSameLenSNP {
            right_ext_add_len++
        }
    }
    left_most_pos := match_pos - e_pos - left_ext_add_len
    if left_most_pos >= 0 {
        ref_left_flank = I.SEQ[left_most_pos : match_pos]
    } else {
        ref_left_flank = I.SEQ[0 : match_pos]
    }
    right_most_pos := (match_pos + lcs_len) + (len(read) - s_pos) - 1 + right_ext_add_len
    if  right_most_pos <= len(I.SEQ) {
        ref_right_flank = I.SEQ[match_pos + lcs_len : right_most_pos]
    } else {
        ref_right_flank = I.SEQ[match_pos + lcs_len : len(I.SEQ)]
    }

    read_left_flank = read[ : e_pos]
    left_d, left_D, left_m, left_n, left_sn, _ :=
     I.BackwardDistance(read_left_flank, ref_left_flank, left_most_pos, align_mem.Bw_snp_idx, align_mem.Bw_snp_val, align_mem.Bw_D, align_mem.Bw_T)

    read_right_flank = read[s_pos + 1 : ]
    right_d, right_D, right_m, right_n, right_sn, _ :=
     I.ForwardDistance(read_right_flank, ref_right_flank, match_pos + lcs_len, align_mem.Fw_snp_idx, align_mem.Fw_snp_val, align_mem.Fw_D, align_mem.Fw_T)

	//fmt.Println("read_left_flank\t", string(read_left_flank))
	//fmt.Println("ref_left_flank\t", string(ref_left_flank), "\t", left_most_pos)
	//fmt.Println("read_right_flank\t", string(read_right_flank))
	//fmt.Println("ref_right_flank\t", string(ref_right_flank), "\t", match_pos + lcs_len)
	//fmt.Println(left_d,"\t", right_d, "\t", left_D, "\t", right_D)

    dis := left_d + right_d + left_D + right_D
    if dis <= DIST_THRES {
        left_num := I.BackwardTraceBack(read_left_flank, ref_left_flank,
         left_m, left_n, left_most_pos, left_sn, align_mem.Bw_snp_idx, align_mem.Bw_snp_val, align_mem.Bw_T)
        right_num := I.ForwardTraceBack(read_right_flank, ref_right_flank,
         right_m, right_n, match_pos + lcs_len, right_sn, align_mem.Fw_snp_idx, align_mem.Fw_snp_val, align_mem.Fw_T)
        return dis, left_num, right_num, true
    }
    return dis, 0, 0, false
}
