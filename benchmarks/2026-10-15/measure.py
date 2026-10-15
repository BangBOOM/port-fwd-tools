#!/usr/bin/env python3
"""macOS resource A/B measurement; generated fixture never contacts remote hosts.

Build bin/pf first with make build, then run this script from any directory.
Use --binary PATH to measure an installed build matching the source.
Only the baseline TUI binary is patched, using a temporary Go build overlay.
The measured pf, repository Go sources and user's SSH configuration are untouched.
"""
import argparse
import ctypes
import datetime
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import pty
import re
import select
import shlex
import shutil
import socket
import statistics
import struct
import subprocess
import tempfile
import termios
import threading
import time

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
MIB = 2 ** 20

class Usage(ctypes.Structure):
    _fields_ = [('uuid', ctypes.c_uint8 * 16)] + [
        (name, ctypes.c_uint64) for name in
        'user system pkg_idle interrupt pageins wired rss footprint start exit'.split()
    ]

class Timebase(ctypes.Structure):
    _fields_ = [('numer', ctypes.c_uint32), ('denom', ctypes.c_uint32)]

LIBPROC = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
LIBPROC.proc_pid_rusage.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_void_p]
LIBSYSTEM = ctypes.CDLL('/usr/lib/libSystem.B.dylib')
LIBSYSTEM.mach_timebase_info.argtypes = [ctypes.POINTER(Timebase)]
TIMEBASE = Timebase()
assert LIBSYSTEM.mach_timebase_info(ctypes.byref(TIMEBASE)) == 0
TICK_SECONDS = TIMEBASE.numer / TIMEBASE.denom / 1e9

def usage(pid):
    u = Usage()
    if LIBPROC.proc_pid_rusage(pid, 0, ctypes.byref(u)) != 0:
        raise OSError(ctypes.get_errno(), 'proc_pid_rusage', pid)
    return dict(cpu_seconds=(u.user + u.system) * TICK_SECONDS,
                rss_mib=u.rss / MIB, footprint_mib=u.footprint / MIB,
                start=u.start, pageins=u.pageins)

def validate_clock():
    before = usage(os.getpid())
    cpu_start = time.process_time()
    end = time.monotonic() + .2
    while time.monotonic() < end:
        pass
    elapsed = time.process_time() - cpu_start
    measured = usage(os.getpid())['cpu_seconds'] - before['cpu_seconds']
    assert abs(measured - elapsed) < .02, (measured, elapsed)
    return dict(api_cpu_seconds=measured, process_time_seconds=elapsed,
                mach_timebase_numer=TIMEBASE.numer, mach_timebase_denom=TIMEBASE.denom)

def command(args, **kwargs):
    return subprocess.check_output(list(map(str, args)), cwd=ROOT, text=True, **kwargs).strip()

class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('localhost', timeout=3)
        self.path = str(path)
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(3)
        self.sock.connect(self.path)

def state(runtime):
    conn = UnixHTTP(runtime / 'daemon.sock')
    try:
        conn.request('POST', '/rpc', json.dumps(dict(action='state')),
                     {'Content-Type': 'application/json'})
        response = conn.getresponse()
        result = json.loads(response.read())
        assert response.status == 200, result
        return result['state']
    finally:
        conn.close()

class UI:
    def __init__(self, binary, home, env):
        self.fd, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
        self.proc = subprocess.Popen([str(binary), '--home', str(home), '--theme', 'terminal'],
            stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
        os.close(slave)
        self.output = b''
        self.reader = threading.Thread(target=self.drain, daemon=True)
        self.reader.start()
    def drain(self):
        try:
            while self.proc.poll() is None:
                if not select.select([self.fd], [], [], .1)[0]:
                    continue
                data = os.read(self.fd, 65536)
                if not data:
                    break
                self.output = (self.output + data)[-131072:]
                if b'\x1b[6n' in data:
                    os.write(self.fd, b'\x1b[1;1R')
                if b'\x1b[c' in data:
                    os.write(self.fd, b'\x1b[?1;2c')
        except OSError:
            pass
    def close(self):
        if self.proc.poll() is None:
            os.write(self.fd, b'q')
            try:
                self.proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.proc.terminate()
                self.proc.wait(timeout=3)
        self.reader.join(timeout=1)
        os.close(self.fd)
        assert self.proc.returncode == 0, self.output.decode('utf-8', 'replace')

class Load:
    def __init__(self, port, rate_mib):
        self.conn = socket.create_connection(('127.0.0.1', port), timeout=3)
        self.conn.settimeout(5)
        self.rate = rate_mib * MIB
        self.sent = self.received = 0
        self.error = None
        self.stop_event = threading.Event()
        self.writer = threading.Thread(target=self.write, daemon=True)
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()
        self.writer.start()
    def write(self):
        payload = os.urandom(64 * 1024)
        start = time.monotonic()
        try:
            while not self.stop_event.is_set():
                delay = start + self.sent / self.rate - time.monotonic()
                if delay > 0 and self.stop_event.wait(delay):
                    break
                self.conn.sendall(payload)
                self.sent += len(payload)
        except OSError as error:
            self.error = repr(error)
    def read(self):
        try:
            while True:
                data = self.conn.recv(256 * 1024)
                if not data:
                    break
                self.received += len(data)
        except OSError as error:
            self.error = repr(error)
    def counters(self):
        assert self.error is None, self.error
        return self.sent, self.received
    def close(self):
        self.stop_event.set()
        self.writer.join(timeout=5)
        # Drain the echo before sending EOF: never confuse fixture shutdown
        # with a forwarding failure or discard the final packet in flight.
        deadline = time.monotonic() + 5
        while self.received < self.sent and time.monotonic() < deadline and self.error is None:
            time.sleep(.005)
        try:
            self.conn.shutdown(socket.SHUT_WR)
        except OSError:
            pass
        self.reader.join(timeout=5)
        self.conn.close()
        assert not self.writer.is_alive() and not self.reader.is_alive(), 'load threads stuck'
        assert self.error is None, self.error
        assert self.sent == self.received, (self.sent, self.received)

def sample(pids, seconds, load):
    begin = {role: usage(pid) for role, pid in pids.items()}
    samples = [{role: usage(pid) for role, pid in pids.items()}]
    totals_begin = load.counters() if load else (0, 0)
    start = time.monotonic()
    next_sample = start + .5
    while True:
        time.sleep(max(0, min(start + seconds, next_sample) - time.monotonic()))
        samples.append({role: usage(pid) for role, pid in pids.items()})
        now = time.monotonic()
        if now >= start + seconds:
            break
        next_sample += .5
    elapsed = time.monotonic() - start
    end = {role: usage(pid) for role, pid in pids.items()}
    totals_end = load.counters() if load else (0, 0)
    components = {}
    for role in pids:
        assert begin[role]['start'] == end[role]['start'], 'process replaced during sample'
        item = dict(cpu_percent=(end[role]['cpu_seconds']-begin[role]['cpu_seconds'])/elapsed*100)
        for metric in ('rss_mib', 'footprint_mib'):
            values = [row[role][metric] for row in samples]
            item[metric+'_mean'] = statistics.mean(values)
            item[metric+'_min'] = min(values)
            item[metric+'_max'] = max(values)
        components[role] = item
    totals = {key: sum(c[key] for c in components.values())
              for key in ('cpu_percent', 'rss_mib_mean', 'footprint_mib_mean')}
    return dict(elapsed_seconds=elapsed, components=components, total=totals,
                sent_mib_s=(totals_end[0]-totals_begin[0])/MIB/elapsed,
                received_mib_s=(totals_end[1]-totals_begin[1])/MIB/elapsed,
                raw_samples=samples)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', default=str(ROOT / 'bin/pf'), help='pf binary matching a trimpath build of the current source')
    parser.add_argument('--seconds', type=float, default=40)
    parser.add_argument('--warmup', type=float, default=8)
    parser.add_argument('--rounds', type=int, default=2)
    parser.add_argument('--rate-mib', type=float, default=10)
    parser.add_argument('--resume', action='store_true', help='keep completed windows from an interrupted run')
    args = parser.parse_args()
    installed = Path(args.binary).expanduser().resolve()
    output = HERE / 'raw.json'
    metadata = dict(started=datetime.datetime.now().astimezone().isoformat(),
        machine=command(['sysctl','-n','machdep.cpu.brand_string']),
        logical_cpus=int(command(['sysctl','-n','hw.ncpu'])),
        memory_bytes=int(command(['sysctl','-n','hw.memsize'])),
        macos=platform.mac_ver()[0], architecture=platform.machine(),
        openssh=command(['/usr/bin/ssh','-V'], stderr=subprocess.STDOUT),
        go=command(['go','version']), installed_version=command([installed,'version']),
        cpu_clock_validation=validate_clock(), parameters=vars(args),
        cpu_convention='100% = one CPU core; delta cumulative user+system time / wall time',
        memory_convention='RSS and physical footprint sampled every 0.5s; MiB = 2^20 bytes',
        baseline='same source/build flags, only pollTraffic early-return enabled via temporary Go overlay',
        scope='client pf TUI + client pf daemon + client OpenSSH master; fixture/server, load generator, terminal emulator excluded')
    data = dict(metadata=metadata, complete=False, trials=[])
    if args.resume:
        data = json.loads(output.read_text())
        assert not data['complete'], 'run already complete'
        for name in ('seconds','warmup','rounds','rate_mib'):
            assert data['metadata']['parameters'][name] == getattr(args,name), name
        metadata = data['metadata']
        metadata.setdefault('resumptions',[]).append(dict(
            started=datetime.datetime.now().astimezone().isoformat(),
            completed_windows=len(data['trials']),
            reason='load drain assertion after window 6; completed windows validated; local fixture half-close and cleanup fixed'))
    def save():
        staged = output.with_suffix('.json.tmp')
        staged.write_text(json.dumps(data, indent=2)+'\n')
        os.replace(staged, output)
    save()
    with tempfile.TemporaryDirectory(prefix='pf-resource-') as tmp:
        work = Path(tmp)
        original = ROOT / 'internal/tui/traffic.go'
        source = original.read_text()
        patched = source.replace('if m.trafficBusy ||', 'if true || m.trafficBusy ||', 1)
        assert patched != source
        overlay_source = work / 'traffic.go'
        overlay_source.write_text(patched)
        overlay = work / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(original):str(overlay_source)}}))
        baseline, current, fixture_binary = (work/name for name in ('pf-no-stats','pf-current','fixture'))
        command(['go','build','-trimpath','-overlay',overlay,'-o',baseline,'./cmd/pf'])
        command(['go','build','-trimpath','-o',current,'./cmd/pf'])
        assert current.read_bytes() == installed.read_bytes(), 'installed binary differs from current source build'
        metadata['installed_sha256'] = hashlib.sha256(installed.read_bytes()).hexdigest()
        metadata['baseline_sha256'] = hashlib.sha256(baseline.read_bytes()).hexdigest()
        command(['go','build','-trimpath','-o',fixture_binary,HERE/'fixture.go'])
        fixture = daemon = ui = load = None
        runtime = None
        try:
            fixture = subprocess.Popen([str(fixture_binary),'--dir',tmp],stdout=subprocess.PIPE,text=True)
            fixture_info = json.loads(fixture.stdout.readline())
            wrapper = work / 'testbin'
            wrapper.mkdir()
            ssh = wrapper / 'ssh'
            ssh.write_text('#!/bin/sh\nexec /usr/bin/ssh -F '+shlex.quote(fixture_info['ssh_config'])+' "$@"\n')
            ssh.chmod(0o755)
            env = dict(os.environ,PATH=str(wrapper)+os.pathsep+os.environ['PATH'],TERM='xterm-256color',COLORTERM='truecolor',LC_ALL='C')
            env.pop('SSH_AUTH_SOCK',None)
            env.pop('NO_COLOR',None)
            home = work / 'home'
            home.mkdir(mode=0o700)
            with socket.socket() as free:
                free.bind(('127.0.0.1',0))
                local_port = free.getsockname()[1]
            (home/'config.toml').write_text("version = 1\n[[tunnels]]\nid = '0123456789abcdef'\nname = 'bench'\nssh_host = 'pf-bench'\nlocal_port = %d\nremote_host = '127.0.0.1'\nremote_port = %d\nenabled = true\nauto_reconnect = true\n"%(local_port,fixture_info['echo_port']))
            runtime = Path('/tmp/pf-%d-%s'%(os.getuid(),hashlib.sha256(str(home).encode()).digest()[:8].hex()))
            with (work/'daemon.log').open('wb') as log:
                daemon = subprocess.Popen([str(installed),'--home',str(home),'daemon','run'],env=env,stdout=log,stderr=log,start_new_session=True)
            ready = None
            deadline = time.monotonic()+10
            while time.monotonic()<deadline:
                try:
                    ready = state(runtime)
                    if ready['tunnels'][0]['status']=='connected':
                        break
                except (OSError,AssertionError):
                    pass
                assert daemon.poll() is None, (work/'daemon.log').read_text()
                time.sleep(.1)
            assert ready and ready['tunnels'][0]['status']=='connected', ready
            children = list(map(int,command(['pgrep','-P',daemon.pid]).split()))
            assert len(children)==1, children
            ssh_pid = children[0]
            fixture_session=dict(tunnels=1,ssh_pid=ssh_pid,daemon_pid=daemon.pid,terminal='80x24, terminal theme, PTY',compression=False,source_unchanged=original.read_text()==source)
            metadata.setdefault('fixture_sessions',[metadata['fixture']] if 'fixture' in metadata else []).append(fixture_session)
            metadata.setdefault('fixture',fixture_session)
            save()
            trials=[]
            for round_number in range(args.rounds):
                phases=['idle','forward'] if round_number%2==0 else ['forward','idle']
                modes=['off','on'] if round_number%2==0 else ['on','off']
                for phase in phases:
                    trials.extend((round_number+1,phase,mode) for mode in modes)
            trials.extend((1,phase,'closed') for phase in ('idle','forward'))
            completed={(t['round'],t['phase'],t['stats']) for t in data['trials']}
            for round_number,phase,mode in trials:
                if (round_number,phase,mode) in completed:
                    continue
                if phase=='forward':
                    load=Load(local_port,args.rate_mib)
                if mode!='closed':
                    ui=UI(installed if mode=='on' else baseline,home,env)
                time.sleep(args.warmup)
                if ui:
                    assert ui.proc.poll() is None, ui.output
                    text=ui.output.decode('utf-8','replace')
                    assert 'Total ↓ / ↑' in text, text
                    if mode=='on':
                        assert re.search(r'\d+(?:\.\d+)?[BKMGTEP]+ / \d+(?:\.\d+)?[BKMGTEP]+',text), text
                pids=dict(daemon=daemon.pid,ssh=ssh_pid)
                if ui:
                    pids['tui']=ui.proc.pid
                print(json.dumps(dict(event='begin',round=round_number,phase=phase,stats=mode,pids=pids)),flush=True)
                trial=sample(pids,args.seconds,load)
                trial.update(round=round_number,phase=phase,stats=mode,pids=pids)
                if phase=='forward':
                    assert abs(trial['sent_mib_s']-args.rate_mib)<args.rate_mib*.05, trial['sent_mib_s']
                    assert abs(trial['received_mib_s']-args.rate_mib)<args.rate_mib*.05, trial['received_mib_s']
                data['trials'].append(trial)
                save()
                print(json.dumps({key:value for key,value in trial.items() if key!='raw_samples'}),flush=True)
                if ui:
                    ui.close();ui=None
                if load:
                    load.close();load=None
            assert original.read_text()==source
            assert len(data['trials']) == len(trials)
            data['complete']=True
            metadata['finished']=datetime.datetime.now().astimezone().isoformat()
            save()
            print('Saved '+str(output),flush=True)
        finally:
            try:
                try:
                    if ui:
                        ui.close()
                finally:
                    if load:
                        load.close()
            finally:
                try:
                    if daemon and daemon.poll() is None:
                        daemon.terminate();daemon.wait(timeout=5)
                finally:
                    if fixture and fixture.poll() is None:
                        fixture.terminate();fixture.wait(timeout=5)
                    if runtime and runtime.exists():
                        shutil.rmtree(runtime)

if __name__=='__main__':
    main()
