namespace GameAgent.Stardew.Tasks;

public interface ITaskNpcDriver
{
    WorldPosition ReadPosition(string npcEntityId);
    DriverResult StartTravel(OperationKey operation, Landmark landmark);
    DriverResult Poll(OperationKey operation);
    void Release(OperationKey operation, string reason);
}
