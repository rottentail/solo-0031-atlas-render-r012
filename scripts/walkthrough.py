#!/usr/bin/env python3
"""Run bounded production flows against a temporary Atlas Render server."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import zlib

ROOT = Path(__file__).resolve().parents[1]


def sample():
    return {"width": 320, "height": 240, "background": "#F5F1E8", "layers": [
        {"id": "plate", "kind": "rectangle", "width": 220, "height": 150,
         "transform": {"x": 45, "y": 35, "scaleX": 1, "scaleY": 1, "degrees": 5},
         "opacity": 1, "fill": {"kind": "linear", "color": "#145A70", "endColor": "#32A5A0", "from": {"x": 0, "y": 0}, "to": {"x": 220, "y": 150}}},
        {"id": "accent", "kind": "ellipse", "width": 130, "height": 130,
         "transform": {"x": 150, "y": 95, "scaleX": 1, "scaleY": 1, "degrees": 0},
         "opacity": .85, "fill": {"kind": "solid", "color": "#E87E4F"}},
        {"id": "inset", "kind": "group", "transform": {"x": 150, "y": 100, "scaleX": 1, "scaleY": 1, "degrees": -8}, "opacity": .9,
         "clip": {"x": 0, "y": 0, "width": 90, "height": 75}, "children": [
             {"id": "facet", "kind": "polygon", "width": 100, "height": 100,
              "transform": {"scaleX": 1, "scaleY": 1}, "opacity": .8,
              "fill": {"kind": "solid", "color": "#FFF4D5"}, "points": [{"x": 10, "y": 90}, {"x": 50, "y": 0}, {"x": 100, "y": 90}]}]}]}


def tiny_png():
    def chunk(tag, data):
        return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2, 2, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(b'\0' + bytes([240, 40, 80, 255] * 2) + b'\0' + bytes([40, 80, 240, 128] * 2))) + chunk(b'IEND', b'')


def badge(color='#E87E4F'):
    """A reusable badge component body: a group with a solid disc and a clip."""
    return {"layers": [
        {"id": "badge-body", "kind": "ellipse", "width": 80, "height": 80,
         "transform": {"x": 10, "y": 10, "scaleX": 1, "scaleY": 1, "degrees": 0},
         "opacity": 1, "fill": {"kind": "solid", "color": color}},
        {"id": "badge-bar", "kind": "rectangle", "width": 90, "height": 14,
         "transform": {"x": 5, "y": 43, "scaleX": 1, "scaleY": 1, "degrees": 0},
         "opacity": .7, "fill": {"kind": "solid", "color": "#FFF4D5"}}]}


def pin(instance_id, component='badge', revision=1, x=10, y=10):
    return {"id": instance_id, "kind": "component", "componentId": component,
            "componentRevision": revision,
            "transform": {"x": x, "y": y, "scaleX": 1, "scaleY": 1, "degrees": 0},
            "opacity": 1}


class Demo:
    def __init__(self, binary, directory):
        self.binary = binary
        self.directory = directory
        self.proc = None
        self.log = open(directory / 'server.log', 'ab')
        self.start()

    def start(self):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.url = f'http://127.0.0.1:{port}'
        env = dict(os.environ, ATLAS_ADDR=f'127.0.0.1:{port}', ATLAS_STATE=str(self.directory / 'state.json'))
        self.proc = subprocess.Popen([str(self.binary)], env=env, stdout=self.log, stderr=self.log)
        for _ in range(100):
            try:
                self.call('GET', '/healthz')
                return
            except (urllib.error.URLError, ConnectionError):
                if self.proc.poll() is not None:
                    raise RuntimeError((self.directory / 'server.log').read_text())
                time.sleep(.03)
        raise RuntimeError('server startup deadline exceeded')

    def stop(self, abrupt=False):
        if self.proc and self.proc.poll() is None:
            (self.proc.kill if abrupt else self.proc.terminate)()
            self.proc.wait(timeout=15)

    def call(self, method, path, body=None, status=200, headers=None):
        hdr = dict(headers or {})
        if isinstance(body, (dict, list)):
            body = json.dumps(body).encode()
            hdr['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.url + path, data=body, method=method, headers=hdr)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: expected {status}, received {response.status}: {data[:500]!r}')
            if response.headers.get('Content-Type', '').startswith('application/json'):
                return json.loads(data)
            return data

    def raw(self, method, path, body):
        """Issue a request and return (status, decoded body) without raising."""
        request = urllib.request.Request(self.url + path, data=json.dumps(body).encode(), method=method,
                                         headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            return response.status, json.loads(data) if data else None

    def wait(self, job_id, wanted=('succeeded',)):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            job = self.call('GET', '/v1/jobs/' + job_id)
            if job['state'] in wanted:
                return job
            if job['state'] in ('failed', 'cancelled'):
                raise RuntimeError(f'unexpected job outcome: {job}')
            time.sleep(.025)
        raise RuntimeError('render deadline exceeded')

    def submit(self, snapshot, key='render-one', scale=1):
        return self.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': scale}, 202, {'Idempotency-Key': key})


def run(demo, workflow, out):
    if workflow in ('components-adopt', 'components-freeze-exchange'):
        return run_component(demo, workflow)
    d = sample()
    put = lambda doc, rev=0: demo.call('PUT', '/v1/scenes/emblem', {'name': '双色印记', 'expectedRevision': rev, 'document': doc})
    scene = put(d)
    if workflow == 'create-scene':
        assert demo.call('GET', '/v1/scenes/emblem')['document'] == scene['document']
        bad = sample(); bad['layers'][0]['transform']['scaleX'] = 0
        demo.call('PUT', '/v1/scenes/invalid', {'name': '无效几何', 'expectedRevision': 0, 'document': bad}, 400)
    elif workflow == 'revise-scene':
        d['background'] = '#F0F0F0'; assert put(d, 1)['revision'] == 2
        demo.call('PUT', '/v1/scenes/emblem', {'name': '旧版本', 'expectedRevision': 1, 'document': d}, 409)
    elif workflow == 'archive-scene':
        archived = demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': 1, 'state': 'archived'})
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': archived['revision']}, 409)
        assert demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': archived['revision'], 'state': 'active'})['revision'] == 3
    if workflow in ('import-asset', 'retire-asset'):
        asset = demo.call('PUT', '/v1/assets/ribbon', tiny_png(), 201)
        assert asset['width'] == 2 and asset['digest'] == hashlib.sha256(tiny_png()).hexdigest()
        demo.call('PUT', '/v1/assets/broken', b'not png', 400)
        layer = {'id': 'ribbon', 'kind': 'image', 'width': 60, 'height': 60, 'assetId': 'ribbon', 'opacity': 1, 'transform': {'x': 20, 'y': 30, 'scaleX': 1, 'scaleY': 1}}
        d['layers'].append(layer); put(d, 1)
        pinned = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 201)
        demo.call('POST', '/v1/assets/ribbon/retire')
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 409)
        result = demo.wait(demo.submit(pinned)['id']); assert result['state'] == 'succeeded'
        return {'workflow': workflow, 'assetDigest': asset['digest'], 'pinnedRender': result['state']}
    current = demo.call('GET', '/v1/scenes/emblem')
    snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision']}, 201)
    if workflow == 'freeze-scene':
        original_digest = snapshot['digest']; d['background'] = '#000000'; put(d, current['revision'])
        assert demo.call('GET', '/v1/snapshots/' + snapshot['id'])['digest'] == original_digest
    if workflow in ('export-package', 'import-package'):
        package = demo.call('GET', '/v1/snapshots/' + snapshot['id'] + '/package')
        restored = demo.call('POST', '/v1/imports/restored', {'name': '恢复的印记', 'package': package}, 201)
        assert restored['document'] == snapshot['document']
        corrupt = json.loads(json.dumps(package)); corrupt['snapshot']['document']['width'] += 1
        demo.call('POST', '/v1/imports/corrupt', {'name': '损坏包', 'package': corrupt}, 400)
        demo.call('GET', '/v1/scenes/corrupt', status=404)
    if workflow in ('cancel-render', 'retry-render', 'recover-render'):
        heavy = sample(); heavy['width'] = 1500; heavy['height'] = 1500
        heavy['layers'] = [dict(heavy['layers'][0], id=f'sheet-{i}', width=1500, height=1500, transform={'scaleX': 1, 'scaleY': 1}) for i in range(16)]
        put(heavy, current['revision'])
        snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
    job = demo.submit(snapshot)
    if workflow == 'submit-render':
        assert demo.submit(snapshot)['id'] == job['id']
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 2}, 409, {'Idempotency-Key': 'render-one'})
    if workflow == 'revoke-snapshot':
        demo.call('POST', '/v1/snapshots/' + snapshot['id'] + '/revoke')
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 1}, 409, {'Idempotency-Key': 'blocked-after-revoke'})
    if workflow in ('cancel-render', 'retry-render'):
        stopped = demo.call('POST', '/v1/jobs/' + job['id'] + '/cancel')
        assert stopped['state'] == 'cancelled'
        if workflow == 'cancel-render':
            time.sleep(.1); assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'cancelled'
            return {'workflow': workflow, 'state': 'cancelled'}
        job = demo.call('POST', '/v1/jobs/' + job['id'] + '/retry', status=202)
        assert job['attempt'] == 2
    if workflow == 'recover-render':
        demo.wait(job['id'], wanted=('running',)); demo.stop(abrupt=True); demo.start()
    result = demo.wait(job['id'])
    png = demo.call('GET', '/v1/artifacts/' + result['artifactId'])
    assert png[:8] == b'\x89PNG\r\n\x1a\n'
    assert struct.unpack('>II', png[16:24]) == (snapshot['document']['width'], snapshot['document']['height'])
    if out:
        Path(out).mkdir(parents=True, exist_ok=True); (Path(out) / 'atlas-sample.png').write_bytes(png)
    if workflow == 'compare-renders':
        other = demo.wait(demo.submit(snapshot, 'render-two')['id'])
        comparison = demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': other['artifactId']})
        assert comparison['changedPixels'] == 0
        d['background'] = '#000000'; put(d, current['revision'])
        altered = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
        changed = demo.wait(demo.submit(altered, 'render-three')['id'])
        assert demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': changed['artifactId']})['changedPixels'] > 0
    if workflow == 'purge-artifact':
        demo.call('DELETE', '/v1/artifacts/' + result['artifactId'])
        demo.call('GET', '/v1/artifacts/' + result['artifactId'], status=410)
        assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'succeeded'
    if workflow == 'observe-service':
        summary = demo.call('GET', '/v1/status'); assert summary['jobs']['succeeded'] >= 1
        events = demo.call('GET', '/v1/events'); assert any(e['kind'] == 'job.succeeded' for e in events)
    return {'workflow': workflow, 'state': result['state'], 'pngBytes': len(png), 'pngDigest': hashlib.sha256(png).hexdigest()}


def put_component(demo, cid, body, expected=0, name='徽章'):
    return demo.call('PUT', '/v1/components/' + cid, {'name': name, 'expectedRevision': expected, 'document': body})


def run_component(demo, workflow):
    """Create a reusable component, adopt it, freeze, disable and exchange it."""
    # A component is authored once and published as immutable revision 1.
    component = put_component(demo, 'badge', badge())
    assert component['revision'] == 1 and component['state'] == 'active'
    # Re-publishing against a stale expectation is rejected.
    demo.call('PUT', '/v1/components/badge', {'name': '徽章', 'expectedRevision': 0, 'document': badge()}, 409)

    # Nested component instances resolve transitively through the closure.
    frame = {"layers": [pin('frame-inner', 'badge', 1, x=6, y=6)]}
    assert put_component(demo, 'frame', frame, 0, '画框')['revision'] == 1
    # Self reference and mutual recursion are rejected with a readable chain.
    demo.call('PUT', '/v1/components/badge', {'name': '徽章', 'expectedRevision': 1, 'document': {"layers": [pin('loop', 'badge', 2)]}}, 400)
    demo.call('PUT', '/v1/components/frame', {'name': '画框', 'expectedRevision': 1, 'document': {"layers": [pin('frame-inner', 'badge', 1, x=6, y=6), pin('frame-self', 'frame', 2, x=12, y=12)]}}, 400)

    # The frozen closure is transitive: pinning only the frame also captures the
    # badge revision that frame revision 1 embeds.
    nested_doc = {'width': 200, 'height': 200, 'background': '#FFFFFF', 'layers': [pin('frame-one', 'frame', 1, x=10, y=10)]}
    nested_scene = demo.call('PUT', '/v1/scenes/nested', {'name': '嵌套画框', 'expectedRevision': 0, 'document': nested_doc})
    nested_snap = demo.call('POST', '/v1/scenes/nested/snapshots', {'expectedRevision': nested_scene['revision']}, 201)
    assert set(nested_snap['components']) == {'frame@1', 'badge@1'}
    nested_package = demo.call('GET', '/v1/snapshots/' + nested_snap['id'] + '/package')
    assert set(nested_package['snapshot']['components']) == {'frame@1', 'badge@1'}
    assert demo.wait(demo.submit(nested_snap, 'nested-one')['id'])['state'] == 'succeeded'
    # The nested badge is reported as an indirect occurrence, not a direct pin.
    nested_usage = {u['componentId']: u for u in demo.call('GET', '/v1/scenes/nested/components')}
    assert nested_usage['frame']['instanceCount'] == 1
    assert nested_usage['badge']['instanceCount'] == 0
    assert nested_usage['badge']['nestedRevisions'] == [1] and nested_usage['badge']['nestedOccurrences'] == 1
    # A component occurring only inside another component revision is not a
    # direct pin of this scene and cannot be adopted here.
    demo.call('POST', '/v1/scenes/nested/components/badge/adopt', {'expectedRevision': nested_scene['revision'], 'revision': 2}, 409)

    # A component may own bitmap layers: freezing and exporting then carry the
    # asset bytes too, because the asset ids are only reachable through the
    # component revision.
    asset = demo.call('PUT', '/v1/assets/ribbon', tiny_png(), 201)
    framed = {'layers': [
        {'id': 'frame-plate', 'kind': 'rectangle', 'width': 60, 'height': 30,
         'transform': {'x': 2, 'y': 2, 'scaleX': 1, 'scaleY': 1, 'degrees': 0}, 'opacity': 1,
         'fill': {'kind': 'solid', 'color': '#145A70'}},
        {'id': 'frame-image', 'kind': 'image', 'width': 40, 'height': 40, 'assetId': 'ribbon',
         'transform': {'x': 8, 'y': 8, 'scaleX': 1, 'scaleY': 1, 'degrees': 0}, 'opacity': 1}]}
    assert put_component(demo, 'picture', framed, 0, '图片')['revision'] == 1
    pic_doc = {'width': 120, 'height': 120, 'background': '#FFFFFF', 'layers': [pin('pic-one', 'picture', 1, x=20, y=20)]}
    pic_scene = demo.call('PUT', '/v1/scenes/picture', {'name': '图片组件', 'expectedRevision': 0, 'document': pic_doc})
    pic_snap = demo.call('POST', '/v1/scenes/picture/snapshots', {'expectedRevision': pic_scene['revision']}, 201)
    assert pic_snap['components']['picture@1']['document']['layers'][1]['assetId'] == 'ribbon'
    assert 'ribbon' in pic_snap['assets'] and pic_snap['assets']['ribbon']['digest'] == asset['digest']
    demo.call('POST', '/v1/assets/ribbon/retire')
    # Retirement blocks new freezes through the component but not the frozen one.
    demo.call('POST', '/v1/scenes/picture/snapshots', {'expectedRevision': pic_scene['revision']}, 409)
    pic_job = demo.wait(demo.submit(pic_snap, 'picture-one')['id'])
    assert pic_job['state'] == 'succeeded'
    pic_package = demo.call('GET', '/v1/snapshots/' + pic_snap['id'] + '/package')
    assert 'ribbon' in pic_package['snapshot']['assets']
    # Importing into a destination that lost the asset is refused as a whole.
    demo.call('POST', '/v1/imports/picture', {'name': '图片导入', 'package': pic_package}, 409)
    demo.call('GET', '/v1/scenes/picture-import', status=404)

    # A scene instantiates the component twice with different local transforms.
    d = sample()
    d['layers'].append(pin('badge-one', 'badge', 1, x=20, y=20))
    d['layers'].append(pin('badge-two', 'badge', 1, x=220, y=120))
    scene = demo.call('PUT', '/v1/scenes/emblem', {'name': '组件印记', 'expectedRevision': 0, 'document': d})
    usage = demo.call('GET', '/v1/scenes/emblem/components')
    assert len(usage) == 1 and usage[0]['instanceCount'] == 2 and not usage[0]['updateAvailable']

    # Instance transforms are local: moving one instance changes only that
    # instance's pixels; the scene geometry itself stays byte-identical.
    moved = json.loads(json.dumps(d))
    moved['layers'][-1]['transform']['x'] = 120
    moved_scene = demo.call('PUT', '/v1/scenes/moved', {'name': '移动实例', 'expectedRevision': 0, 'document': moved})
    moved_snap = demo.call('POST', '/v1/scenes/moved/snapshots', {'expectedRevision': moved_scene['revision']}, 201)
    moved_job = demo.wait(demo.submit(moved_snap, 'moved-one')['id'])

    # Freeze revision 1 and render it: the frozen bytes come from the closure.
    frozen = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': scene['revision']}, 201)
    assert 'badge@1' in frozen['components'] and frozen['components']['badge@1']['revision'] == 1
    before = demo.wait(demo.submit(frozen, 'frozen-one')['id'])
    before_png = demo.call('GET', '/v1/artifacts/' + before['artifactId'])
    shift = demo.call('POST', '/v1/compare', {'left': before['artifactId'], 'right': moved_job['artifactId']})
    assert shift['changedPixels'] > 0
    assert demo.call('POST', '/v1/compare', {'left': moved_job['artifactId'], 'right': before['artifactId']})['maxDelta'] == shift['maxDelta']

    # Publishing revision 2 must not change existing scenes or snapshots.
    updated = put_component(demo, 'badge', badge('#145A70'), 1)
    assert updated['revision'] == 2
    usage = demo.call('GET', '/v1/scenes/emblem/components')
    assert usage[0]['updateAvailable'] and usage[0]['revision'] == 1
    assert [l['componentRevision'] for l in demo.call('GET', '/v1/scenes/emblem')['document']['layers'] if l.get('componentId') == 'badge'] == [1, 1]
    assert demo.call('GET', '/v1/snapshots/' + frozen['id'])['digest'] == frozen['digest']
    again = demo.wait(demo.submit(frozen, 'frozen-two')['id'])
    assert demo.call('POST', '/v1/compare', {'left': before['artifactId'], 'right': again['artifactId']})['changedPixels'] == 0

    # A plain document edit cannot retarget an existing instance...
    original_pin = d['layers'][-1]['componentRevision']
    d['layers'][-1] = pin('badge-two', 'badge', 2, x=220, y=120)
    demo.call('PUT', '/v1/scenes/emblem', {'name': '绕过采用', 'expectedRevision': scene['revision'], 'document': d}, 409)
    d['layers'][-1] = pin('badge-two', 'badge', original_pin, x=220, y=120)
    # ...but authoring a brand-new instance at any published revision is fine.
    d['layers'].append(pin('badge-three', 'badge', 2, x=120, y=20))
    scene = demo.call('PUT', '/v1/scenes/emblem', {'name': '组件印记', 'expectedRevision': scene['revision'], 'document': d})
    assert scene['revision'] == 2

    # Adoption is optimistic: a stale scene revision is rejected.
    demo.call('POST', '/v1/scenes/emblem/components/badge/adopt',
              {'expectedRevision': scene['revision'] + 5, 'revision': 2}, 409)
    results = []
    barrier = threading.Barrier(3)

    def concurrent_adopt():
        barrier.wait()
        results.append(('adopt',) + demo.raw('POST', '/v1/scenes/emblem/components/badge/adopt',
                                             {'expectedRevision': scene['revision'], 'revision': 2}))

    def concurrent_edit():
        barrier.wait()
        edit = json.loads(json.dumps(d))
        # Rename only: the document keeps revision 1 pins, so whichever request
        # wins the published content is fully determined and never a merge.
        results.append(('edit',) + demo.raw('PUT', '/v1/scenes/emblem',
                                            {'name': '并行编辑', 'expectedRevision': scene['revision'], 'document': edit}))

    threads = [threading.Thread(target=concurrent_adopt), threading.Thread(target=concurrent_edit)]
    for t in threads: t.start()
    barrier.wait()
    for t in threads: t.join()
    outcomes = sorted((status, kind) for kind, status, _ in results)
    assert outcomes in ([(200, 'adopt'), (409, 'edit')], [(200, 'edit'), (409, 'adopt')]), outcomes
    winner_kind = next(kind for kind, status, _ in results if status == 200)
    loser = next(body for kind, status, body in results if status == 409)
    assert loser['code'] == 'conflict'
    published = demo.call('GET', '/v1/scenes/emblem')
    # The published scene carries exactly one next revision, the winner's
    # content, and no mixture of adopted and unadopted pins.
    assert published['revision'] == scene['revision'] + 1
    if winner_kind == 'adopt':
        assert published['name'] == '组件印记'
        assert [l['componentRevision'] for l in published['document']['layers'] if l.get('componentId') == 'badge'] == [2, 2, 2]
        assert published['componentAdoptions'][-1]['fromRevisions'] == [1, 2]
        assert published['componentAdoptions'][-1]['toRevision'] == 2
    else:
        assert published['name'] == '并行编辑'
        assert [l['componentRevision'] for l in published['document']['layers'] if l.get('componentId') == 'badge'] == [1, 1, 2]
        assert not published.get('componentAdoptions')
        # The edit won; adoption can now proceed on that committed revision and
        # moves both revision-1 pins while leaving the already-current one.
        published = demo.call('POST', '/v1/scenes/emblem/components/badge/adopt',
                              {'expectedRevision': published['revision'], 'revision': 2})
        assert published['componentAdoptions'][-1]['fromRevisions'] == [1, 2]
    adopted = published
    assert [l['componentRevision'] for l in adopted['document']['layers'] if l.get('componentId') == 'badge'] == [2, 2, 2]
    replay = demo.call('POST', '/v1/scenes/emblem/components/badge/adopt',
                       {'expectedRevision': adopted['revision'], 'revision': 2})
    assert replay['revision'] == adopted['revision']

    # The new scene revision renders the new component; the frozen one does not.
    fresh = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': adopted['revision']}, 201)
    assert fresh['digest'] != frozen['digest'] and fresh['components']['badge@2']['revision'] == 2
    after = demo.wait(demo.submit(fresh, 'frozen-three')['id'])
    delta = demo.call('POST', '/v1/compare', {'left': before['artifactId'], 'right': after['artifactId']})
    assert delta['changedPixels'] > 0
    if workflow == 'components-adopt':
        return {'workflow': workflow, 'sceneRevision': adopted['revision'],
                'frozenDigest': frozen['digest'], 'adoptedDigest': fresh['digest'],
                'changedPixels': delta['changedPixels'], 'pngBytes': len(before_png)}

    # 交叉场景: a second scene pinned at revision 1 adopts independently.
    other = demo.call('PUT', '/v1/scenes/copy', {'name': '共享徽章', 'expectedRevision': 0, 'document': {'width': 320, 'height': 240, 'background': '#F5F1E8', 'layers': [pin('badge-copy', 'badge', 1, x=40, y=40)]}})
    assert demo.call('GET', '/v1/scenes/copy/components')[0]['updateAvailable']
    other = demo.call('POST', '/v1/scenes/copy/components/badge/adopt', {'expectedRevision': other['revision'], 'revision': 2})
    assert demo.call('GET', '/v1/scenes/emblem')['revision'] == adopted['revision']

    # Disabling is metadata only: forward production stops, frozen work continues.
    disabled = demo.call('POST', '/v1/components/badge/state', {'expectedRevision': 2, 'state': 'disabled'})
    assert disabled['state'] == 'disabled'
    # A disabled component accepts no new revisions while it stays disabled.
    demo.call('PUT', '/v1/components/badge', {'name': '徽章', 'expectedRevision': 2, 'document': badge('#000000')}, 409)
    demo.call('POST', '/v1/scenes/emblem/components/badge/adopt', {'expectedRevision': adopted['revision'], 'revision': 2}, 409)
    demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': adopted['revision']}, 409)
    blocked = demo.call('PUT', '/v1/scenes/new-scene', {'name': '新引用', 'expectedRevision': 0, 'document': {'width': 64, 'height': 64, 'background': '#FFFFFF', 'layers': [pin('badge-new', 'badge', 2)]}}, 409)
    assert blocked['code'] == 'conflict'
    still = demo.wait(demo.submit(frozen, 'frozen-four')['id'])
    assert still['state'] == 'succeeded'
    assert demo.call('POST', '/v1/compare', {'left': before['artifactId'], 'right': still['artifactId']})['changedPixels'] == 0

    # Persisted disabled state and pinned closures survive a process restart;
    # the frozen snapshot still renders while forward production stays blocked.
    demo.stop(); demo.start()
    assert demo.call('GET', '/v1/components/badge')['state'] == 'disabled'
    assert demo.call('GET', '/v1/scenes/emblem')['revision'] == adopted['revision']
    restarted = demo.wait(demo.submit(frozen, 'frozen-after-restart')['id'])
    assert restarted['state'] == 'succeeded'
    demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': adopted['revision']}, 409)

    # A disabled component in a nested closure blocks a new scene too.
    demo.call('PUT', '/v1/scenes/nested-scene', {'name': '嵌套引用', 'expectedRevision': 0, 'document': {'width': 64, 'height': 64, 'background': '#FFFFFF', 'layers': [pin('outer', 'frame', 1)]}}, 409)

    # Exchange: the package carries the complete component closure. Importing
    # into a destination where the component is disabled is refused as a whole.
    package = demo.call('GET', '/v1/snapshots/' + frozen['id'] + '/package')
    assert set(package['snapshot']['components']) == {'badge@1'}
    demo.call('POST', '/v1/imports/restored', {'name': '恢复组件', 'package': package}, 409)
    demo.call('GET', '/v1/scenes/restored', status=404)

    # Re-enabling allows publication again, but adoption is forward-only.
    active = demo.call('POST', '/v1/components/badge/state', {'expectedRevision': 2, 'state': 'active'})
    assert active['state'] == 'active'
    third = put_component(demo, 'badge', badge('#32A5A0'), 2)
    assert third['revision'] == 3
    demo.call('POST', '/v1/scenes/emblem/components/badge/adopt', {'expectedRevision': adopted['revision'], 'revision': 1}, 409)
    current = demo.call('GET', '/v1/scenes/emblem')
    final = demo.call('POST', '/v1/scenes/emblem/components/badge/adopt', {'expectedRevision': current['revision'], 'revision': 3})
    assert final['revision'] == current['revision'] + 1

    # The frozen package still imports at its pinned revision 1 and renders to
    # the same pixels, independent of the destination's current revision 3.
    restored = demo.call('POST', '/v1/imports/restored', {'name': '恢复组件', 'package': package}, 201)
    assert restored['document']['layers'][-1]['componentRevision'] == 1
    packed = demo.wait(demo.submit(demo.call('POST', '/v1/scenes/restored/snapshots', {'expectedRevision': restored['revision']}, 201), 'restored-one')['id'])
    assert demo.call('POST', '/v1/compare', {'left': before['artifactId'], 'right': packed['artifactId']})['changedPixels'] == 0
    restored_usage = demo.call('GET', '/v1/scenes/restored/components')
    assert restored_usage[0]['revision'] == 1 and restored_usage[0]['updateAvailable']
    tampered = json.loads(json.dumps(package))
    tampered['snapshot']['components']['badge@1']['document']['layers'][0]['fill']['color'] = '#000000'
    demo.call('POST', '/v1/imports/tampered', {'name': '篡改组件', 'package': tampered}, 400)
    demoted = json.loads(json.dumps(package))
    demoted['snapshot']['document']['layers'][-1]['componentRevision'] = 2
    demo.call('POST', '/v1/imports/demoted', {'name': '篡改引脚', 'package': demoted}, 400)
    demo.call('GET', '/v1/scenes/tampered', status=404)
    demo.call('GET', '/v1/scenes/demoted', status=404)

    assert demo.call('GET', '/v1/status')['componentCount'] == 3
    return {'workflow': workflow, 'sceneRevision': final['revision'], 'componentRevision': 3,
            'frozenDigest': frozen['digest'], 'changedPixels': delta['changedPixels'],
            'restoredRender': packed['state'], 'pngBytes': len(before_png)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', default='submit-render')
    parser.add_argument('--out')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='atlas-walkthrough-') as temp:
        directory = Path(temp); binary = directory / 'atlas'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/atlas'], cwd=ROOT, check=True, timeout=60)
        demo = Demo(binary, directory)
        try:
            print(json.dumps(run(demo, args.workflow, args.out), ensure_ascii=False))
        finally:
            demo.stop(); demo.log.close()


if __name__ == '__main__':
    main()
