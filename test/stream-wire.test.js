// stream-wire.test.js — v2 frame decoding, pinned against the same literals
// as stream_test.go (TestAppendV2SpotsFrame).

import { describe, it, expect, beforeEach } from 'vitest';

import { decodeSpotsFrame, exactSpotKey, __resetLatLngCache, __latLngCacheSize } from '../static/stream-wire.js';

// data: line of the Go pin: two spots at server time 1000.
const GO_PIN = '{"n":1000,"s":[["JO62","20m",-12,437],["EM12","40m",-20,100,"","w"]]}';

describe('decodeSpotsFrame', () => {
    beforeEach(() => __resetLatLngCache());

    it('rebuilds the v1 spot shape from the Go-pinned frame', () => {
        const { n, spots } = decodeSpotsFrame(GO_PIN);
        expect(n).toBe(1000);
        expect(spots).toHaveLength(2);
        expect(spots[0]).toEqual({
            lat: 52.5, lng: 13, snr: -12, ageSeconds: 437, locator: 'JO62',
            sourceType: 'mqtt', band: '20m', __t: 563,
        });
        expect(spots[1]).toMatchObject({
            lat: 32.5, lng: -97, snr: -20, ageSeconds: 100, locator: 'EM12',
            sourceType: 'wspr', band: '40m', __t: 900,
        });
        expect('reporterLocator' in spots[1]).toBe(false); // "" placeholder -> absent, like v1 omitempty
    });

    it('maps source codes and optional fields', () => {
        const { spots } = decodeSpotsFrame(
            '{"n":50,"s":[["K","20m",0,0,"","d","DL1ABC","K1XYZ"],["EM12","20m",20,1,"JO31","r"],["AB","15m",1,2,"FN31","other"]]}');
        expect(spots[0]).toMatchObject({ sourceType: 'dxcluster', sender: 'DL1ABC', receiver: 'K1XYZ', lat: 0, lng: 0 });
        expect(spots[1]).toMatchObject({ sourceType: 'rbn', reporterLocator: 'JO31' });
        expect(spots[1].sender).toBeUndefined();
        expect(spots[2].sourceType).toBe('other');
    });

    it('clamps negative ages but keeps the exact time', () => {
        const { spots } = decodeSpotsFrame('{"n":1000,"s":[["JO62","20m",1,-5]]}');
        expect(spots[0].ageSeconds).toBe(0);
        expect(spots[0].__t).toBe(1005);
    });

    it('maps a too-short locator to 0,0 like the Go encoder', () => {
        const { spots } = decodeSpotsFrame('{"n":1,"s":[["","20m",1,0]]}');
        expect(spots[0]).toMatchObject({ lat: 0, lng: 0 });
    });

    it('returns null for malformed frames and skips bad tuples', () => {
        expect(decodeSpotsFrame('not json')).toBeNull();
        expect(decodeSpotsFrame('{"s":[]}')).toBeNull();
        expect(decodeSpotsFrame('{"n":1}')).toBeNull();
        const { spots } = decodeSpotsFrame('{"n":9,"s":[["JO62","20m",1],["JO62","20m","x",0],["JO62","20m",1,2]]}');
        expect(spots).toHaveLength(1);
    });

    it('memoizes lat/lng per locator with a bounded cache', () => {
        decodeSpotsFrame('{"n":1,"s":[["JO62","20m",1,0],["JO62","40m",1,0],["FN31","20m",1,0]]}');
        expect(__latLngCacheSize()).toBe(2);
    });
});

describe('exactSpotKey', () => {
    it('is stable for redelivered spots and distinct for different ones', () => {
        const a = decodeSpotsFrame(GO_PIN).spots[0];
        // Redelivery in a later frame: age grows with n, __t stays.
        const b = decodeSpotsFrame('{"n":1060,"s":[["JO62","20m",-12,497]]}').spots[0];
        expect(exactSpotKey(a)).toBe(exactSpotKey(b));
        const c = decodeSpotsFrame('{"n":1000,"s":[["JO62","20m",-11,437]]}').spots[0];
        expect(exactSpotKey(a)).not.toBe(exactSpotKey(c));
    });

    it('falls back to the supplied time for v1 spots', () => {
        expect(exactSpotKey({ band: '20m', locator: 'JO62', snr: 1 }, 42)).toContain('|42');
    });
});
