//! Calendar arithmetic on Unix time, in UTC or at a fixed offset from it. devbox writes and
//! reads a handful of timestamps; that does not need a time zone database.

/// A moment broken into the fields of the calendar.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Civil {
    /// The year.
    pub year: i64,
    /// The month, 1 to 12.
    pub month: u32,
    /// The day of the month, 1 to 31.
    pub day: u32,
    /// The hour, 0 to 23.
    pub hour: u32,
    /// The minute.
    pub minute: u32,
    /// The second.
    pub second: u32,
    /// The day of the week, 0 for Sunday to 6 for Saturday.
    pub weekday: u32,
}

/// The calendar fields of `secs` seconds since the Unix epoch, in UTC.
#[must_use]
pub fn civil(secs: i64) -> Civil {
    let (days, rest) = (secs.div_euclid(86400), secs.rem_euclid(86400));
    // Civil date from a day count (Howard Hinnant's algorithm), with eras starting 0000-03-01.
    let z = days + 719_468;
    let (era, day_of_era) = (z.div_euclid(146_097), z.rem_euclid(146_097));
    let year_of_era =
        (day_of_era - day_of_era / 1460 + day_of_era / 36524 - day_of_era / 146_096) / 365;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let shifted_month = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * shifted_month + 2) / 5 + 1;
    let month = if shifted_month < 10 { shifted_month + 3 } else { shifted_month - 9 };
    let small = |value: i64| u32::try_from(value).unwrap_or(0);
    Civil {
        year: year_of_era + era * 400 + i64::from(month <= 2),
        month: small(month),
        day: small(day),
        hour: small(rest / 3600),
        minute: small(rest % 3600 / 60),
        second: small(rest % 60),
        // 1970-01-01 was a Thursday.
        weekday: small((days + 4).rem_euclid(7)),
    }
}

/// Seconds since the Unix epoch of a calendar date and time in UTC.
#[must_use]
pub fn unix(year: i64, month: u32, day: u32, hour: u32, minute: u32, second: u32) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let (era, year_of_era) = (year.div_euclid(400), year.rem_euclid(400));
    let shifted_month = i64::from(if month > 2 { month - 3 } else { month + 9 });
    let day_of_year = (153 * shifted_month + 2) / 5 + i64::from(day) - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    let days = era * 146_097 + day_of_era - 719_468;
    days * 86400 + i64::from(hour) * 3600 + i64::from(minute) * 60 + i64::from(second)
}

/// `secs` as an RFC 3339 timestamp in UTC: `2026-09-21T14:13:20Z`.
#[must_use]
pub fn rfc3339(secs: i64) -> String {
    let c = civil(secs);
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
        c.year, c.month, c.day, c.hour, c.minute, c.second
    )
}

/// The date of `secs`, `offset` seconds east of UTC: `2026-09-21`.
#[must_use]
pub fn date(secs: i64, offset: i64) -> String {
    let c = civil(secs + offset);
    format!("{:04}-{:02}-{:02}", c.year, c.month, c.day)
}

/// Seconds since the Unix epoch of an RFC 3339 timestamp (`2026-09-21T14:13:20.123Z`,
/// `2026-09-21T16:13:20+02:00`); `None` for anything else.
#[must_use]
pub fn parse_rfc3339(text: &str) -> Option<i64> {
    let number = |range: std::ops::Range<usize>| text.get(range)?.parse::<u32>().ok();
    let bytes = text.as_bytes();
    if bytes.len() < 20
        || bytes[4] != b'-'
        || bytes[7] != b'-'
        || !matches!(bytes[10], b'T' | b't' | b' ')
    {
        return None;
    }
    let year = i64::from(number(0..4)?);
    let at = unix(
        year,
        number(5..7)?,
        number(8..10)?,
        number(11..13)?,
        number(14..16)?,
        number(17..19)?,
    );
    let zone = text[19..].trim_start_matches(|c: char| c == '.' || c.is_ascii_digit());
    match zone.as_bytes() {
        [b'Z' | b'z'] => Some(at),
        [sign @ (b'+' | b'-'), ..] if zone.len() == 6 && zone.as_bytes()[3] == b':' => {
            let offset = i64::from(zone[1..3].parse::<u32>().ok()?) * 3600
                + i64::from(zone[4..6].parse::<u32>().ok()?) * 60;
            Some(if *sign == b'+' { at - offset } else { at + offset })
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::{civil, date, parse_rfc3339, rfc3339, unix};

    #[test]
    fn timestamps_are_utc() {
        assert_eq!(rfc3339(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339(951_782_399), "2000-02-28T23:59:59Z");
        assert_eq!(rfc3339(951_782_400), "2000-02-29T00:00:00Z");
        assert_eq!(rfc3339(1_790_000_000), "2026-09-21T14:13:20Z");
        assert_eq!(rfc3339(4_102_444_799), "2099-12-31T23:59:59Z");
        assert_eq!(rfc3339(-1), "1969-12-31T23:59:59Z");
    }

    #[test]
    fn weekdays_and_offsets() {
        assert_eq!(civil(0).weekday, 4); // a Thursday
        assert_eq!(civil(1_790_000_000).weekday, 1); // a Monday
        assert_eq!(date(1_790_000_000, 0), "2026-09-21");
        assert_eq!(date(1_790_000_000, 10 * 3600), "2026-09-22");
        assert_eq!(date(1_790_000_000, -15 * 3600), "2026-09-20");
    }

    #[test]
    fn calendar_round_trips() {
        for secs in [0, 86_399, 951_782_400, 1_790_000_000, 4_102_444_799, -86_400] {
            let c = civil(secs);
            assert_eq!(unix(c.year, c.month, c.day, c.hour, c.minute, c.second), secs);
        }
    }

    #[test]
    fn rfc3339_is_parsed_with_fractions_and_offsets() {
        assert_eq!(parse_rfc3339("2026-09-21T14:13:20Z"), Some(1_790_000_000));
        assert_eq!(parse_rfc3339("2026-09-21T14:13:20.123456Z"), Some(1_790_000_000));
        assert_eq!(parse_rfc3339("2026-09-21T16:13:20+02:00"), Some(1_790_000_000));
        assert_eq!(parse_rfc3339("2026-09-21T09:43:20.5-04:30"), Some(1_790_000_000));
        for bad in [
            "",
            "2026-09-21",
            "2026-09-21T14:13:20",
            "yesterday at about noon, I think",
            "2026-09-21T14:13:20+0200",
        ] {
            assert_eq!(parse_rfc3339(bad), None, "{bad}");
        }
    }
}
