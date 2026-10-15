#!/usr/bin/env python3
"""Aggregate the completed macOS resource experiment without hiding repetitions."""
import json
from pathlib import Path
import statistics

here = Path(__file__).resolve().parent
data = json.loads((here / 'raw.json').read_text())
assert data['complete'], 'measurement has not finished'

def mean(values):
    return statistics.mean(list(values))

def aggregate(trials):
    rss_samples = [sum(item['rss_mib'] for item in row.values())
                   for trial in trials for row in trial['raw_samples']]
    footprint_samples = [sum(item['footprint_mib'] for item in row.values())
                         for trial in trials for row in trial['raw_samples']]
    return dict(repetitions=len(trials),
        seconds=sum(t['elapsed_seconds'] for t in trials),
        total_cpu_percent=mean(t['total']['cpu_percent'] for t in trials),
        total_cpu_percent_runs=[t['total']['cpu_percent'] for t in trials],
        pf_cpu_percent=mean(sum(v['cpu_percent'] for role,v in t['components'].items() if role!='ssh') for t in trials),
        ssh_cpu_percent=mean(t['components']['ssh']['cpu_percent'] for t in trials),
        tui_cpu_percent=mean(t['components'].get('tui',{}).get('cpu_percent',0) for t in trials),
        total_rss_mib=mean(t['total']['rss_mib_mean'] for t in trials),
        total_rss_mib_range=[min(rss_samples),max(rss_samples)],
        total_footprint_mib=mean(t['total']['footprint_mib_mean'] for t in trials),
        total_footprint_mib_range=[min(footprint_samples),max(footprint_samples)],
        tui_rss_mib=mean(t['components'].get('tui',{}).get('rss_mib_mean',0) for t in trials),
        tui_footprint_mib=mean(t['components'].get('tui',{}).get('footprint_mib_mean',0) for t in trials),
        sent_mib_s=mean(t['sent_mib_s'] for t in trials),
        received_mib_s=mean(t['received_mib_s'] for t in trials))

summary=dict(metadata=data['metadata'],scenarios={},differences={})
for phase in ('idle','forward'):
    summary['scenarios'][phase]={}
    for mode in ('off','on','closed'):
        trials=[t for t in data['trials'] if t['phase']==phase and t['stats']==mode]
        summary['scenarios'][phase][mode]=aggregate(trials)
    off=summary['scenarios'][phase]['off']
    on=summary['scenarios'][phase]['on']
    summary['differences'][phase]={key:on[key]-off[key] for key in
        ('pf_cpu_percent','ssh_cpu_percent','tui_cpu_percent','total_cpu_percent',
         'total_rss_mib','total_footprint_mib','tui_rss_mib','tui_footprint_mib')}
    summary['differences'][phase]['paired_total_cpu_percentage_points']=[]
    summary['differences'][phase]['paired_pf_cpu_percentage_points']=[]
    for round_number in range(1,data['metadata']['parameters']['rounds']+1):
        pair={t['stats']:t for t in data['trials'] if t['phase']==phase and t['round']==round_number and t['stats']!='closed'}
        summary['differences'][phase]['paired_total_cpu_percentage_points'].append(pair['on']['total']['cpu_percent']-pair['off']['total']['cpu_percent'])
        pf=lambda t:sum(v['cpu_percent'] for role,v in t['components'].items() if role!='ssh')
        summary['differences'][phase]['paired_pf_cpu_percentage_points'].append(pf(pair['on'])-pf(pair['off']))
(here/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
for phase,modes in summary['scenarios'].items():
    for mode,row in modes.items():
        print('%-7s %-6s PF CPU %.4f%%; SSH CPU %.4f%%; RSS %.2f MiB (%.2f–%.2f); footprint %.2f MiB; up/down %.3f/%.3f MiB/s'%(
            phase,mode,row['pf_cpu_percent'],row['ssh_cpu_percent'],row['total_rss_mib'],*row['total_rss_mib_range'],row['total_footprint_mib'],row['sent_mib_s'],row['received_mib_s']))
print('On minus off:')
print(json.dumps(summary['differences'],indent=2))
