import { describe, expect, it } from 'vitest';

import { feedLagSummary, parseFeedLag } from './feed-lag.js';

describe('parseFeedLag', () => {
    it('is null while the feed is on time', () => {
        expect(parseFeedLag({ feed_lag_seconds: 0, pskr_feed_lag_seconds: 0 })).toBeNull();
        expect(parseFeedLag({})).toBeNull();
        expect(parseFeedLag(null)).toBeNull();
    });

    it('reads the delay and the shift in minutes', () => {
        expect(parseFeedLag({ feed_lag_seconds: 825, pskr_feed_lag_seconds: 825 })).toEqual({ lagMin: 14, shiftMin: 14 });
        expect(parseFeedLag({ feed_lag_seconds: 0, pskr_feed_lag_seconds: 825 })).toEqual({ lagMin: 14, shiftMin: 0 });
    });
});

describe('feedLagSummary', () => {
    it('is empty without a delay', () => {
        expect(feedLagSummary(null, 15)).toBe('');
    });

    it('says where Now ends when the window moved', () => {
        expect(feedLagSummary({ lagMin: 14, shiftMin: 14 }, 15))
            .toBe('PSKReporter reports arrive about 14 min late, so Now ends 14 min ago.');
    });

    it('warns when the time range could not move back far enough', () => {
        expect(feedLagSummary({ lagMin: 14, shiftMin: 0 }, 60))
            .toBe('PSKReporter reports arrive about 14 min late, so Now ends 14 min ago. The band figures for the 60-min time range cannot move back that far and read low.');
    });
});
