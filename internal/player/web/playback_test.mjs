import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import test from 'node:test';
import assert from 'node:assert/strict';

const html = readFileSync(new URL('./musickit.html', import.meta.url), 'utf8');
function functionSource(name) {
 const start = html.indexOf(`async function ${name}(`);
 if (start < 0) return '';
 const end = html.indexOf('\n        }', start) + '\n        }'.length;
 return html.slice(start, end);
}
function setup(music) {
 const errors = [];
 const env = {
  window: {}, _m: () => music, _log: () => {},
  _stateN: [], _qi: 0, _q: [{id:'123'}], _busy: false,
  _wantIdx: -1, _wantNative: false, _nativeQueue: false, _nativeMirrors: false,
  _playPending: null, _removedIds: new Set(),
  _playAt: () => {}, _stopAndWait: async () => {}, _warmLibItems: () => {},
  _runPending: () => {}, errName: e => e.message,
  goError: text => { errors.push(text); return Promise.resolve(); },
 };
 const start = html.indexOf('window.vibezPlay = async () =>');
 const end = html.indexOf('window.vibezPause = async () =>', start);
 runInNewContext(functionSource('_ensurePlaying') + '\n' + html.slice(start, end) + '\n' + functionSource('_doPlayNativeAt'), env);
 return {env, errors};
}
function music(state) {
 return {
  playbackState: state, nowPlayingItem: {id:'123'}, plays:0, pauses:0, stops:0,
  async play() {
   this.plays++;
   if (![3,4].includes(this.playbackState)) throw new Error('The play() method was called without a previous stop() or pause() call.');
   await new Promise(resolve => setTimeout(resolve, 1));
   this.playbackState = 2;
  },
  async pause() { this.pauses++; this.playbackState=3; },
  async stop() { this.stops++; this.playbackState=4; },
  async setQueue() { this.playbackState=2; },
 };
}
test('play on an already playing track is a no-op', async () => {
 const m=music(2); const {env,errors}=setup(m);
 await env.window.vibezPlay();
 assert.deepEqual(errors, []); assert.equal(m.plays,0);
});
test('concurrent resume requests call MusicKit play once', async () => {
 const m=music(3); const {env,errors}=setup(m);
 await Promise.all([env.window.vibezPlay(),env.window.vibezPlay()]);
 assert.deepEqual(errors, []); assert.equal(m.plays,1); assert.equal(m.stops,0);
});
test('resume from loading pauses before play without resetting the track', async () => {
 const m=music(1); const {env,errors}=setup(m);
 await env.window.vibezPlay();
 assert.deepEqual(errors, []); assert.equal(m.pauses,1); assert.equal(m.stops,0);
 assert.equal(m.playbackState,2);
});
test('native setQueue auto-play does not trigger a duplicate play and fallback', async () => {
 const m=music(4); const {env}=setup(m);
 await env._doPlayNativeAt(0);
 assert.equal(env._nativeQueue,true); assert.equal(m.plays,0);
});
