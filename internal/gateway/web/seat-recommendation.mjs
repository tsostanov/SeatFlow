export function recommendSeat(seatIDs, availableSeatIDs, columns = 8) {
  if (
    !Array.isArray(seatIDs) ||
    !Array.isArray(availableSeatIDs) ||
    !Number.isSafeInteger(columns) ||
    columns < 1
  )
    return null;

  const available = new Set(availableSeatIDs.map(String));
  const center = (columns - 1) / 2;
  let recommendation = null;
  let bestScore = Number.POSITIVE_INFINITY;
  seatIDs.map(String).forEach((id, index) => {
    if (!available.has(id)) return;
    const row = Math.floor(index / columns);
    const column = index % columns;
    // Whole rows beat later rows; within a row the center wins.
    const score = row * columns + Math.abs(column - center);
    if (score < bestScore) {
      bestScore = score;
      recommendation = id;
    }
  });
  return recommendation;
}
